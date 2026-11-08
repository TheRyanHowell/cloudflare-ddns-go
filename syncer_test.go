package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

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

func newTestSyncer(record *fakeRecord, lookup *fakeLookup) *syncer {
	return &syncer{
		logger:            discardLogger(),
		record:            record,
		lookup:            lookup,
		health:            newHealth(),
		interval:          time.Millisecond,
		startupRetryDelay: time.Millisecond,
	}
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
			got, err := newTestSyncer(tt.record, tt.lookup).sync(context.Background(), tt.known)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("sync() = %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
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
	err := newTestSyncer(record, &fakeLookup{}).run(context.Background())
	if !errors.Is(err, denied) {
		t.Errorf("run() = %v, want %v", err, denied)
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

	if err := s.run(ctx); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if len(record.contentErrs) != 0 {
		t.Errorf("transient failures not retried: %d left", len(record.contentErrs))
	}
	if _, ok := s.health.age(); !ok {
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

	if err := s.run(ctx); err != nil {
		t.Fatalf("run() = %v", err)
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
			if err := newTestSyncer(record, &fakeLookup{}).run(ctx); err != nil {
				t.Errorf("run() = %v, want nil", err)
			}
		})
	}
}

func TestPermanent(t *testing.T) {
	wrap := func(err error) error { return errors.Join(errFake, err) }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"401", &fetch.StatusError{Code: 401}, true},
		{"403", &fetch.StatusError{Code: 403}, true},
		{"404", &fetch.StatusError{Code: 404}, true},
		{"429", &fetch.StatusError{Code: 429}, false},
		{"500", &fetch.StatusError{Code: 500}, false},
		{"invalid record", errInvalidRecord, true},
		{"wrapped status", wrap(&fetch.StatusError{Code: 403}), true},
		{"network error", errFake, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permanent(tt.err); got != tt.want {
				t.Errorf("permanent(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRunPermanentUpdateFailureIsFatal(t *testing.T) {
	revoked := &fetch.StatusError{Code: 401, Status: "401 Unauthorized"}
	record := &fakeRecord{content: "1.1.1.1", updateErrs: []error{revoked}}
	err := newTestSyncer(record, &fakeLookup{ips: []string{"2.2.2.2"}}).run(context.Background())
	if !errors.Is(err, errPermanentSync) || !errors.Is(err, revoked) {
		t.Errorf("run() = %v, want permanent sync failure wrapping %v", err, revoked)
	}
}

func TestRunLookupStatusErrorIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	forbidden := &fetch.StatusError{Code: 403, Status: "403 Forbidden"}
	lookup := &fakeLookup{ips: []string{"2.2.2.2"}, err: forbidden, cancel: cancel}
	record := &fakeRecord{content: "1.1.1.1"}
	// The IP service answering 403 is transient from the daemon's view.
	if err := newTestSyncer(record, lookup).run(ctx); err != nil {
		t.Errorf("run() = %v, want nil", err)
	}
}

func TestSyncerWaitsBeforeExitingOnPermanentFailure(t *testing.T) {
	denied := &fetch.StatusError{Code: 403, Status: "403 Forbidden"}
	s := newTestSyncer(&fakeRecord{contentErrs: []error{denied}}, &fakeLookup{})
	s.exitDelay = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("run returned %v at once; want it to wait before exiting", err)
	case <-time.After(50 * time.Millisecond):
	}
	if age, ok := s.health.age(); ok || s.health.failure() == nil {
		t.Errorf("health age = %v, %v, failure %v; want a recorded permanent failure", age, ok, s.health.failure())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run() = %v after shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop when cancelled during the wait")
	}
}
