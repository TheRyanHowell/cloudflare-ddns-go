package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Endpoints are variables so tests can point them at a local server.
var (
	cloudflareApiBaseUrl = "https://api.cloudflare.com/client/v4"
	publicIpUrl          = "https://icanhazip.com/"
)

type RequestModel struct {
	Content string `json:"content"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl"`
}

type config struct {
	apiKey     string
	zoneId     string
	recordId   string
	recordName string
}

// requiredEnvVars lists the environment variables main needs, in check order.
var requiredEnvVars = []string{
	"CLOUDFLARE_API_KEY",
	"CLOUDFLARE_ZONE_ID",
	"CLOUDFLARE_RECORD_ID",
	"CLOUDFLARE_RECORD_NAME",
}

// loadConfig reads the configuration from the environment and returns the
// name of the first missing variable, or "" when all are set.
func loadConfig() (config, string) {
	for _, name := range requiredEnvVars {
		if os.Getenv(name) == "" {
			return config{}, name
		}
	}
	return config{
		apiKey:     os.Getenv("CLOUDFLARE_API_KEY"),
		zoneId:     os.Getenv("CLOUDFLARE_ZONE_ID"),
		recordId:   os.Getenv("CLOUDFLARE_RECORD_ID"),
		recordName: os.Getenv("CLOUDFLARE_RECORD_NAME"),
	}, ""
}

const pollInterval = 5 * time.Minute

func main() {
	cfg, missing := loadConfig()

	if missing != "" {
		fmt.Println(missing + " is not set")
		return
	}

	fmt.Println("Getting current record value")

	publicIp := getDnsRecordValue(cfg.apiKey, cfg.zoneId, cfg.recordId)

	fmt.Println("Current record value: ", publicIp)

	for {
		fmt.Println("Sleeping for 5 minutes")
		<-time.After(pollInterval)

		publicIp = syncRecord(cfg, publicIp)
	}
}

// syncRecord updates the DNS record if the public ip differs from knownIp and
// returns the ip the record now holds.
func syncRecord(cfg config, knownIp string) string {
	fmt.Println("Getting current public ip")
	newPublicIp := getCurrentPublicIp()

	if newPublicIp == knownIp {
		fmt.Println("Public ip has not changed")
		return knownIp
	}

	fmt.Println("Updating record to current public ip: ", newPublicIp)

	updateDnsRecordValue(cfg.apiKey, cfg.zoneId, cfg.recordId, cfg.recordName, newPublicIp)

	fmt.Println("Updated record successfully")
	return newPublicIp
}

func updateDnsRecordValue(apiKey string, zoneId string, dnsRecordId string, recordName string, publicIp string) {
	url := cloudflareApiBaseUrl + "/zones/" + zoneId + "/dns_records/" + dnsRecordId

	request := RequestModel{
		Content: publicIp,
		Name:    recordName,
		Type:    "A",
		TTL:     300,
	}

	payload, _ := json.Marshal(request)

	req, _ := http.NewRequest("PUT", url, bytes.NewBuffer(payload))

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+apiKey)

	client := &http.Client{}

	resp, err := client.Do(req)

	if err != nil {
		panic(err)
	}

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			panic(err)
		}
	}(resp.Body)

	if resp.StatusCode == 200 {
		return
	}

	panic("Failed to update DNS record: " + resp.Status)
}

type DnsRecord struct {
	Content string `json:"content"`
}

type DnsRecordResponse struct {
	Result DnsRecord `json:"result"`
}

func getDnsRecordValue(apiKey string, zoneId string, dnsRecordId string) string {
	url := cloudflareApiBaseUrl + "/zones/" + zoneId + "/dns_records/" + dnsRecordId

	req, _ := http.NewRequest("GET", url, nil)

	req.Header.Add("Authorization", "Bearer "+apiKey)

	client := &http.Client{}

	resp, err := client.Do(req)

	if err != nil {
		panic(err)
	}

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			panic(err)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		panic("Failed to get DNS record: " + resp.Status)
	}

	body, _ := io.ReadAll(resp.Body)

	var response DnsRecordResponse

	err = json.Unmarshal(body, &response)

	if err != nil {
		panic(err)
	}

	return response.Result.Content
}

func getCurrentPublicIp() string {
	resp, err := http.Get(publicIpUrl)

	if err != nil {
		panic(err)
	}

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			panic(err)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		panic("Failed to get public ip: " + resp.Status)
	}

	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)

	ipAddr := strings.TrimSpace(buf.String())

	parsedIp := net.ParseIP(ipAddr)

	if parsedIp.To4() == nil {
		panic("Failed to parse public ip: " + ipAddr)
	}

	return parsedIp.To4().String()
}
