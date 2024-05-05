package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/YutaroHayakawa/go-radv"
)

type Client struct {
	*http.Client
	host string
}

func NewClient(host string) *Client {
	return &Client{
		Client: &http.Client{},
		host:   host,
	}
}

func (c *Client) Reload(config *radv.Config) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "http://"+c.host+"/reload", bytes.NewBuffer(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Successful reload
	if res.StatusCode == http.StatusOK {
		return nil
	}

	// 5XX errors. No error body.
	if res.StatusCode == http.StatusInternalServerError {
		return fmt.Errorf(res.Status)
	}

	// Failed to reload
	var e Error

	if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
		return fmt.Errorf("failed to decode error response: %s", err)
	}

	return &e
}

func (c *Client) Status() (*radv.Status, error) {
	res, err := c.Get("http://" + c.host + "/status")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusOK {
		var status radv.Status
		if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
			return nil, fmt.Errorf("failed to decode status response: %s", err)
		}
		return &status, nil
	}

	if res.StatusCode == http.StatusInternalServerError {
		return nil, fmt.Errorf(res.Status)
	}

	var e Error

	if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
		return nil, fmt.Errorf("failed to decode error response: %s", err)
	}

	return nil, &e
}
