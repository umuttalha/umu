package dns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	token  string
	zoneID string
}

func New(token, zoneID string) *Client {
	return &Client{token: token, zoneID: zoneID}
}

type recordRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

func (c *Client) do(method, path string, body interface{}) ([]byte, error) {
	url := "https://api.cloudflare.com/client/v4" + path

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("cloudflare %s %s: %d %s", method, path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

type listResponse struct {
	Result []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
	} `json:"result"`
}

type createResponse struct {
	Result struct {
		ID string `json:"id"`
	} `json:"result"`
}

func (c *Client) Setup(projectName, ipv6 string) error {
	// Check if AAAA record already exists
	fullName := projectName

	listBody, err := c.do("GET", fmt.Sprintf("/zones/%s/dns_records?type=AAAA&name=%s", c.zoneID, fullName), nil)
	if err != nil {
		return err
	}

	var existing listResponse
	json.Unmarshal(listBody, &existing)

	// If exists, update it
	if len(existing.Result) > 0 {
		recordID := existing.Result[0].ID
		req := recordRequest{
			Type:    "AAAA",
			Name:    fullName,
			Content: ipv6,
			TTL:     120,
			Proxied: false,
		}
		_, err := c.do("PUT", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, recordID), req)
		return err
	}

	// Create new record
	req := recordRequest{
		Type:    "AAAA",
		Name:    fullName,
		Content: ipv6,
		TTL:     120,
		Proxied: false,
	}
	_, err = c.do("POST", fmt.Sprintf("/zones/%s/dns_records", c.zoneID), req)
	return err
}

func (c *Client) SetupA(domain, ipv4 string) error {
	listBody, err := c.do("GET", fmt.Sprintf("/zones/%s/dns_records?type=A&name=%s", c.zoneID, domain), nil)
	if err != nil {
		return err
	}

	var existing listResponse
	json.Unmarshal(listBody, &existing)

	if len(existing.Result) > 0 {
		recordID := existing.Result[0].ID
		req := recordRequest{
			Type:    "A",
			Name:    domain,
			Content: ipv4,
			TTL:     120,
			Proxied: false,
		}
		_, err := c.do("PUT", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, recordID), req)
		return err
	}

	req := recordRequest{
		Type:    "A",
		Name:    domain,
		Content: ipv4,
		TTL:     120,
		Proxied: false,
	}
	_, err = c.do("POST", fmt.Sprintf("/zones/%s/dns_records", c.zoneID), req)
	return err
}

func (c *Client) Teardown(projectName string) error {
	fullName := projectName

	listBody, err := c.do("GET", fmt.Sprintf("/zones/%s/dns_records?type=AAAA&name=%s", c.zoneID, fullName), nil)
	if err != nil {
		return err
	}

	var existing listResponse
	json.Unmarshal(listBody, &existing)

	for _, rec := range existing.Result {
		_, err := c.do("DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, rec.ID), nil)
		if err != nil {
			return err
		}
	}
	return nil
}
