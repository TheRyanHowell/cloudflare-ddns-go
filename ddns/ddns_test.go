package ddns

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheRyanHowell/cloudflare-ddns-go/cloudflare"
	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/fetch"
)

var errFake = errors.New("fake failure")

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

type fakeRecord struct {
	content     string
	contentErrs []error // consumed one per Content call
	updateErrs  []error // consumed one per Update call
	updates     []string
}

func (f *fakeRecord) Content(context.Context) (string, error) {
	if len(f.contentErrs) == 0 {
		return f.content, nil
	}
	err := f.contentErrs[0]
	f.contentErrs = f.contentErrs[1:]
	return "", err
}

func (f *fakeRecord) Update(_ context.Context, ip string) error {
	f.updates = append(f.updates, ip)
	if len(f.updateErrs) == 0 {
		return nil
	}
	err := f.updateErrs[0]
	f.updateErrs = f.updateErrs[1:]
	return err
}

// fakeLookup returns ips in order, then cancels the context when exhausted.
type fakeLookup struct {
	ips    []string
	err    error
	cancel context.CancelFunc
}

func (f *fakeLookup) Current(context.Context) (string, error) {
	if len(f.ips) == 0 {
		if f.cancel != nil {
			f.cancel()
		}
		return "", errFake
	}
	ip := f.ips[0]
	f.ips = f.ips[1:]
	return ip, f.err
}

// fakeHealth records what the syncer reports.
type fakeHealth struct {
	mu        sync.Mutex
	successes int
	failure   error
}

func (f *fakeHealth) MarkSuccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes++
}

func (f *fakeHealth) MarkPermanentFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure = err
}

func (f *fakeHealth) state() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.successes, f.failure
}

func newTestSyncer(record *fakeRecord, lookup *fakeLookup) *Syncer {
	return New(record, lookup,
		WithLogger(discardLogger()),
		WithHealthReporter(&fakeHealth{}),
		WithPermanentCheck(cloudflare.IsPermanent),
		WithInterval(time.Millisecond),
		WithStartupRetryDelay(time.Millisecond),
		WithExitDelay(0),
	)
}

func TestSyncerSync(t *testing.T) {
	tests := []struct {
		name        string
		known       string
		lookup      *fakeLookup
		record      *fakeRecord
		want        string
		wantUpdates []string
		wantErr     bool
	}{
		{name: "unchanged", known: "4.4.4.4", lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, record: &fakeRecord{}, want: "4.4.4.4"},
		{name: "changed", known: "1.1.1.1", lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, record: &fakeRecord{}, want: "4.4.4.4", wantUpdates: []string{"4.4.4.4"}},
		{name: "lookup fails", known: "1.1.1.1", lookup: &fakeLookup{ips: []string{"4.4.4.4"}, err: errFake}, record: &fakeRecord{}, want: "1.1.1.1", wantErr: true},
		{name: "update fails", known: "1.1.1.1", lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, record: &fakeRecord{updateErrs: []error{errFake}}, want: "1.1.1.1", wantUpdates: []string{"4.4.4.4"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newTestSyncer(tt.record, tt.lookup).syncFrom(context.Background(), tt.known)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("syncFrom() = %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
			if !slices.Equal(tt.record.updates, tt.wantUpdates) {
				t.Errorf("updates = %v, want %v", tt.record.updates, tt.wantUpdates)
			}
		})
	}
}

func TestRunPermanentStartupFailureIsFatal(t *testing.T) {
	denied := &fetch.StatusError{Code: 403, Status: "403 Forbidden"}
	record := &fakeRecord{contentErrs: []error{denied, denied}}
	err := newTestSyncer(record, &fakeLookup{}).Run(context.Background())
	if !errors.Is(err, denied) {
		t.Errorf("Run() = %v, want %v", err, denied)
	}
	if len(record.contentErrs) != 1 {
		t.Errorf("Content called %d times, want 1", 2-len(record.contentErrs))
	}
}

func TestRunRetriesTransientStartupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record := &fakeRecord{content: "1.1.1.1", contentErrs: []error{errFake, errFake}}
	s := newTestSyncer(record, &fakeLookup{ips: []string{"1.1.1.1"}, cancel: cancel})

	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if len(record.contentErrs) != 0 {
		t.Errorf("transient failures not retried: %d left", len(record.contentErrs))
	}
	if successes, _ := s.health.(*fakeHealth).state(); successes == 0 {
		t.Error("health not marked after successful startup")
	}
}

func TestRunSyncsImmediatelyAndRetriesAfterFailedUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record := &fakeRecord{content: "1.1.1.1", updateErrs: []error{errFake}}
	s := newTestSyncer(record, &fakeLookup{ips: []string{"2.2.2.2", "2.2.2.2", "2.2.2.2"}, cancel: cancel})

	var logs bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))

	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	want := []string{"2.2.2.2", "2.2.2.2"}
	if !slices.Equal(record.updates, want) {
		t.Fatalf("updates = %v, want %v", record.updates, want)
	}
	if got := strings.Count(logs.String(), "sync failed"); got != 1 {
		t.Errorf("logged %d sync failures, want 1 (cancelled sync must not log)", got)
	}
}

func TestRunCancelled(t *testing.T) {
	tests := map[string]*fakeRecord{
		"before startup read": {contentErrs: []error{context.Canceled}},
		"after startup read":  {content: "1.1.1.1"},
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := newTestSyncer(record, &fakeLookup{}).Run(ctx); err != nil {
				t.Errorf("Run() = %v, want nil", err)
			}
		})
	}
}

func TestRunPermanentUpdateFailureIsFatal(t *testing.T) {
	revoked := &fetch.StatusError{Code: 401, Status: "401 Unauthorized"}
	record := &fakeRecord{content: "1.1.1.1", updateErrs: []error{revoked}}
	err := newTestSyncer(record, &fakeLookup{ips: []string{"2.2.2.2"}}).Run(context.Background())
	if !errors.Is(err, errPermanentSync) || !errors.Is(err, revoked) {
		t.Errorf("Run() = %v, want permanent sync failure wrapping %v", err, revoked)
	}
}

func TestRunLookupStatusErrorIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	forbidden := &fetch.StatusError{Code: 403, Status: "403 Forbidden"}
	lookup := &fakeLookup{ips: []string{"2.2.2.2"}, err: forbidden, cancel: cancel}
	record := &fakeRecord{content: "1.1.1.1"}
	// The IP service answering 403 is transient from the daemon's view.
	if err := newTestSyncer(record, lookup).Run(ctx); err != nil {
		t.Errorf("Run() = %v, want nil", err)
	}
}

func TestSyncerWaitsBeforeExitingOnPermanentFailure(t *testing.T) {
	denied := &fetch.StatusError{Code: 403, Status: "403 Forbidden"}
	s := newTestSyncer(&fakeRecord{contentErrs: []error{denied}}, &fakeLookup{})
	s.exitDelay = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Run returned %v at once; want it to wait before exiting", err)
	case <-time.After(50 * time.Millisecond):
	}
	if successes, failure := s.health.(*fakeHealth).state(); successes != 0 || failure == nil {
		t.Errorf("health successes = %d, failure %v; want a recorded permanent failure", successes, failure)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() = %v after shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when cancelled during the wait")
	}
}

func TestSyncerSyncReadsThenUpdates(t *testing.T) {
	tests := []struct {
		name        string
		record      *fakeRecord
		lookup      *fakeLookup
		wantIP      string
		wantChanged bool
		wantErr     bool
		wantUpdates []string
	}{
		{name: "unchanged", record: &fakeRecord{content: "4.4.4.4"}, lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, wantIP: "4.4.4.4"},
		{name: "changed", record: &fakeRecord{content: "1.1.1.1"}, lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, wantIP: "4.4.4.4", wantChanged: true, wantUpdates: []string{"4.4.4.4"}},
		{name: "read fails", record: &fakeRecord{contentErrs: []error{errFake}}, lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, wantErr: true},
		{name: "update fails", record: &fakeRecord{content: "1.1.1.1", updateErrs: []error{errFake}}, lookup: &fakeLookup{ips: []string{"4.4.4.4"}}, wantErr: true, wantUpdates: []string{"4.4.4.4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, changed, err := newTestSyncer(tt.record, tt.lookup).Sync(context.Background())
			if ip != tt.wantIP || changed != tt.wantChanged || (err != nil) != tt.wantErr {
				t.Errorf("Sync() = %q, %v, %v; want %q, %v, error %v", ip, changed, err, tt.wantIP, tt.wantChanged, tt.wantErr)
			}
			if !slices.Equal(tt.record.updates, tt.wantUpdates) {
				t.Errorf("updates = %v, want %v", tt.record.updates, tt.wantUpdates)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	s := New(&fakeRecord{}, &fakeLookup{})
	if s.interval != defaultInterval || s.startupRetryDelay != defaultStartupRetryDelay || s.exitDelay != defaultExitDelay || s.logger == nil {
		t.Errorf("New() = %+v, want the default timings and a logger", s)
	}
	if s.isPermanent(errFake) {
		t.Error("default permanent check classified an error as permanent")
	}
	s.health.MarkSuccess()
	s.health.MarkPermanentFailure(errFake)
}
