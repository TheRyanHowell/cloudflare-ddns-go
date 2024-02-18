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

type RequestModel struct {
	Content string `json:"content"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl"`
}

func main() {
	apiKey := os.Getenv("CLOUDFLARE_API_KEY")

	if apiKey == "" {
		fmt.Println("CLOUDFLARE_API_KEY is not set")
		return
	}

	zoneId := os.Getenv("CLOUDFLARE_ZONE_ID")

	if zoneId == "" {
		fmt.Println("CLOUDFLARE_ZONE_ID is not set")
		return
	}

	recordId := os.Getenv("CLOUDFLARE_RECORD_ID")

	if recordId == "" {
		fmt.Println("CLOUDFLARE_RECORD_ID is not set")
		return
	}

	recordName := os.Getenv("CLOUDFLARE_RECORD_NAME")

	if recordName == "" {
		fmt.Println("CLOUDFLARE_RECORD_NAME is not set")
		return
	}

	fmt.Println("Getting current record value")

	publicIp := getDnsRecordValue(apiKey, zoneId, recordId)

	fmt.Println("Current record value: ", publicIp)

	for {
		fmt.Println("Sleeping for 5 minutes")
		<-time.After(5 * time.Minute)

		fmt.Println("Getting current public ip")
		newPublicIp := getCurrentPublicIp()

		if newPublicIp == publicIp {
			fmt.Println("Public ip has not changed")
			continue
		}

		fmt.Println("Updating record to current public ip: ", newPublicIp)

		updateDnsRecordValue(apiKey, zoneId, recordId, recordName, newPublicIp)
		publicIp = newPublicIp

		fmt.Println("Updated record successfully")
	}
}

func updateDnsRecordValue(apiKey string, zoneId string, dnsRecordId string, recordName string, publicIp string) {
	url := "https://api.cloudflare.com/client/v4/zones/" + zoneId + "/dns_records/" + dnsRecordId

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
	url := "https://api.cloudflare.com/client/v4/zones/" + zoneId + "/dns_records/" + dnsRecordId

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
	resp, err := http.Get("https://icanhazip.com/")

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
