package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	tibberGraphQLURL = "https://api.tibber.com/v1-beta/gql"
	tibberUserAgent  = "GARO-RPi/1.0 garo-energy-controller/0.3.0"

	tibberMinDiscoveryInterval = 60 * time.Second
	tibberMaxBackoff           = 5 * time.Minute
)

type TibberSnapshot struct {
	Configured bool `json:"configured"`
	Connected  bool `json:"connected"`

	HomeID   string `json:"home_id,omitempty"`
	HomeName string `json:"home_name,omitempty"`

	MeasurementTimestamp string `json:"measurement_timestamp,omitempty"`
	LastUpdate           string `json:"last_update,omitempty"`
	AgeSeconds           int64  `json:"age_seconds"`

	PowerW           float64 `json:"power_w"`
	PowerProductionW float64 `json:"power_production_w"`

	AccumulatedConsumptionLastHourKWh float64 `json:"accumulated_consumption_last_hour_kwh"`
	AccumulatedProductionLastHourKWh  float64 `json:"accumulated_production_last_hour_kwh"`

	Error string `json:"error,omitempty"`

	APIRequestCount          uint64 `json:"api_request_count"`
	LastAPIRequest           string `json:"last_api_request,omitempty"`
	LastAPIRequestAgeSeconds int64  `json:"last_api_request_age_seconds"`
	LastAPIResult            string `json:"last_api_result,omitempty"`
}

type TibberClient struct {
	token string

	mu sync.RWMutex

	configured bool
	connected  bool

	homeID   string
	homeName string

	measurementTimestamp string
	lastUpdate           time.Time

	powerW           float64
	powerProductionW float64

	accumulatedConsumptionLastHourKWh float64
	accumulatedProductionLastHourKWh  float64

	err string

	discoveryMu          sync.Mutex
	cachedDiscovery      *tibberDiscovery
	lastDiscoveryAttempt time.Time

	apiRequestCount uint64
	lastAPIRequest  time.Time
	lastAPIResult   string
}

func (t *TibberClient) noteAPIRequest() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.apiRequestCount++
	t.lastAPIRequest = time.Now()
	t.lastAPIResult = "pending"
}

func (t *TibberClient) noteAPIResult(result string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastAPIResult = result
}

func NewTibberClient(token string) *TibberClient {
	return &TibberClient{
		token:      token,
		configured: token != "",
	}
}
func (t *TibberClient) Run(
	ctx context.Context,
	getConfig func() Config,
) {
	if t.token == "" {
		t.setError(
			fmt.Errorf("Tibber token not configured"),
		)
		return
	}

	backoff := 2 * time.Second

	var discovery tibberDiscovery
	haveDiscovery := false
	forceDiscovery := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cfg := getConfig()

		if !haveDiscovery || forceDiscovery {
			d, err := t.getDiscovery(
				ctx,
				cfg.TibberHomeID,
				forceDiscovery,
			)

			if err != nil {
				t.setError(err)

				log.Printf(
					"Tibber discovery failed: %v",
					err,
				)

				delay := backoff
				if delay > tibberMaxBackoff {
					delay = tibberMaxBackoff
				}

				timer := time.NewTimer(delay)

				select {
				case <-ctx.Done():
					timer.Stop()
					return

				case <-timer.C:
				}

				if backoff < tibberMaxBackoff {
					backoff *= 2

					if backoff > tibberMaxBackoff {
						backoff = tibberMaxBackoff
					}
				}

				continue
			}

			discovery = d
			haveDiscovery = true
			forceDiscovery = false
		}

		hadData, err := t.connectOnce(
			ctx,
			cfg,
			discovery,
		)

		if err == nil {
			backoff = 2 * time.Second
			continue
		}

		t.setError(err)

		if hadData {
			// The cached websocket URL was working.
			// Reuse it without another GraphQL request.
			backoff = 2 * time.Second
		} else {
			// Connection failed before receiving live data.
			// On the next attempt refresh discovery, but
			// getDiscovery() enforces the 60-second minimum.
			forceDiscovery = true
		}

		delay := backoff

		if delay > tibberMaxBackoff {
			delay = tibberMaxBackoff
		}

		jitterRange := delay / 2
		var jitter time.Duration

		if jitterRange > 0 {
			jitter = time.Duration(
				time.Now().UnixNano() %
					int64(jitterRange),
			)
		}

		delay += jitter

		log.Printf(
			"Tibber disconnected: %v; retrying in %s",
			err,
			delay.Round(time.Second),
		)

		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()
			return

		case <-timer.C:
		}

		if backoff < tibberMaxBackoff {
			backoff *= 2

			if backoff > tibberMaxBackoff {
				backoff = tibberMaxBackoff
			}
		}
	}
}
func (t *TibberClient) getDiscovery(
	ctx context.Context,
	preferredHomeID string,
	forceRefresh bool,
) (tibberDiscovery, error) {

	t.discoveryMu.Lock()

	if !forceRefresh && t.cachedDiscovery != nil {
		if preferredHomeID == "" ||
			t.cachedDiscovery.Home.ID == preferredHomeID {

			discovery := *t.cachedDiscovery
			t.discoveryMu.Unlock()
			return discovery, nil
		}
	}

	var wait time.Duration

	if !t.lastDiscoveryAttempt.IsZero() {
		wait = tibberMinDiscoveryInterval -
			time.Since(t.lastDiscoveryAttempt)
	}

	t.discoveryMu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return tibberDiscovery{}, ctx.Err()

		case <-timer.C:
		}
	}

	t.discoveryMu.Lock()
	t.lastDiscoveryAttempt = time.Now()
	t.discoveryMu.Unlock()

	discovery, err := t.discover(preferredHomeID)
	if err != nil {
		return tibberDiscovery{}, err
	}

	t.discoveryMu.Lock()
	t.cachedDiscovery = &discovery
	t.discoveryMu.Unlock()

	return discovery, nil
}
func (t *TibberClient) Snapshot() TibberSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s := TibberSnapshot{
		Configured: t.configured,
		Connected:  t.connected,

		HomeID:   t.homeID,
		HomeName: t.homeName,

		MeasurementTimestamp: t.measurementTimestamp,

		PowerW:           t.powerW,
		PowerProductionW: t.powerProductionW,

		AccumulatedConsumptionLastHourKWh: t.accumulatedConsumptionLastHourKWh,

		AccumulatedProductionLastHourKWh: t.accumulatedProductionLastHourKWh,

		APIRequestCount: t.apiRequestCount,
		LastAPIResult:   t.lastAPIResult,

		Error: t.err,
	}

	if !t.lastUpdate.IsZero() {
		s.LastUpdate = t.lastUpdate.Format(time.RFC3339)
		s.AgeSeconds = int64(time.Since(t.lastUpdate).Seconds())

		if s.AgeSeconds < 0 {
			s.AgeSeconds = 0
		}
	}

	if !t.lastAPIRequest.IsZero() {
		s.LastAPIRequest = t.lastAPIRequest.Format(time.RFC3339)
		s.LastAPIRequestAgeSeconds =
			int64(time.Since(t.lastAPIRequest).Seconds())

		if s.LastAPIRequestAgeSeconds < 0 {
			s.LastAPIRequestAgeSeconds = 0
		}
	}
	return s
}

func (t *TibberClient) setError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connected = false

	if err == nil {
		t.err = ""
	} else {
		t.err = err.Error()
	}
}

func (t *TibberClient) setConnected(
	homeID string,
	homeName string,
) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connected = true
	t.homeID = homeID
	t.homeName = homeName
	t.err = ""
}

type tibberLiveMeasurement struct {
	Timestamp *string `json:"timestamp"`

	Power           *float64 `json:"power"`
	PowerProduction *float64 `json:"powerProduction"`

	AccumulatedConsumptionLastHour *float64 `json:"accumulatedConsumptionLastHour"`
	AccumulatedProductionLastHour  *float64 `json:"accumulatedProductionLastHour"`
}

func (t *TibberClient) setMeasurement(
	m tibberLiveMeasurement,
) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connected = true
	t.err = ""
	t.lastUpdate = time.Now()

	if m.Timestamp != nil {
		t.measurementTimestamp = *m.Timestamp
	}

	if m.Power != nil {
		t.powerW = *m.Power
	}

	if m.PowerProduction != nil {
		t.powerProductionW = *m.PowerProduction
	}

	if m.AccumulatedConsumptionLastHour != nil {
		t.accumulatedConsumptionLastHourKWh =
			*m.AccumulatedConsumptionLastHour
	}

	if m.AccumulatedProductionLastHour != nil {
		t.accumulatedProductionLastHourKWh =
			*m.AccumulatedProductionLastHour
	}
}

type tibberHome struct {
	ID          string `json:"id"`
	AppNickname string `json:"appNickname"`

	Features struct {
		RealTimeConsumptionEnabled bool `json:"realTimeConsumptionEnabled"`
	} `json:"features"`
}

type tibberDiscovery struct {
	WebsocketURL string
	Home         tibberHome
}

func (t *TibberClient) discover(
	preferredHomeID string,
) (tibberDiscovery, error) {

	query := `
query {
  viewer {
    websocketSubscriptionUrl
    homes {
      id
      appNickname
      features {
        realTimeConsumptionEnabled
      }
    }
  }
}`

	requestBody, err := json.Marshal(map[string]string{
		"query": query,
	})
	if err != nil {
		return tibberDiscovery{}, err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		tibberGraphQLURL,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return tibberDiscovery{}, err
	}

	req.Header.Set(
		"Authorization",
		"Bearer "+t.token,
	)
	req.Header.Set(
		"Content-Type",
		"application/json",
	)
	req.Header.Set(
		"User-Agent",
		tibberUserAgent,
	)

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	t.noteAPIRequest()

	resp, err := client.Do(req)
	if err != nil {
		t.noteAPIResult("error: " + err.Error())
		return tibberDiscovery{}, err
	}

	t.noteAPIResult(fmt.Sprintf("HTTP %d", resp.StatusCode))

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return tibberDiscovery{}, err
	}

	if resp.StatusCode < 200 ||
		resp.StatusCode >= 300 {

		return tibberDiscovery{}, fmt.Errorf(
			"Tibber GraphQL HTTP %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var response struct {
		Data struct {
			Viewer struct {
				WebsocketSubscriptionURL string       `json:"websocketSubscriptionUrl"`
				Homes                    []tibberHome `json:"homes"`
			} `json:"viewer"`
		} `json:"data"`

		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return tibberDiscovery{}, err
	}

	if len(response.Errors) > 0 {
		return tibberDiscovery{}, fmt.Errorf(
			"Tibber GraphQL error: %s",
			response.Errors[0].Message,
		)
	}

	if response.Data.Viewer.WebsocketSubscriptionURL == "" {
		return tibberDiscovery{},
			fmt.Errorf("Tibber returned no websocket URL")
	}

	var candidates []tibberHome

	for _, home := range response.Data.Viewer.Homes {
		if home.Features.RealTimeConsumptionEnabled {
			candidates = append(candidates, home)
		}
	}

	if preferredHomeID != "" {
		for _, home := range candidates {
			if home.ID == preferredHomeID {
				return tibberDiscovery{
					WebsocketURL: response.Data.Viewer.WebsocketSubscriptionURL,
					Home:         home,
				}, nil
			}
		}

		return tibberDiscovery{}, fmt.Errorf(
			"Tibber home %q not found or has no real-time device",
			preferredHomeID,
		)
	}

	switch len(candidates) {
	case 0:
		return tibberDiscovery{},
			fmt.Errorf(
				"no Tibber home has real-time consumption enabled",
			)

	case 1:
		return tibberDiscovery{
			WebsocketURL: response.Data.Viewer.WebsocketSubscriptionURL,
			Home:         candidates[0],
		}, nil

	default:
		var homes []string

		for _, home := range candidates {
			name := home.AppNickname

			if name == "" {
				name = "(unnamed)"
			}

			homes = append(
				homes,
				fmt.Sprintf("%s [%s]", name, home.ID),
			)
		}

		return tibberDiscovery{}, fmt.Errorf(
			"multiple Tibber homes have real-time data; set tibber_home_id: %s",
			strings.Join(homes, ", "),
		)
	}
}

type tibberWSMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (t *TibberClient) sendPong(
	conn *websocket.Conn,
	payload json.RawMessage,
) error {

	message := map[string]interface{}{
		"type": "pong",
	}

	if len(payload) > 0 {
		message["payload"] = json.RawMessage(payload)
	}

	return conn.WriteJSON(message)
}

func (t *TibberClient) connectOnce(
	ctx context.Context,
	cfg Config,
	discovery tibberDiscovery,
) (bool, error) {

	headers := http.Header{}

	headers.Set(
		"Authorization",
		"Bearer "+t.token,
	)
	headers.Set(
		"User-Agent",
		tibberUserAgent,
	)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,

		Subprotocols: []string{
			"graphql-transport-ws",
		},
	}

	conn, response, err := dialer.DialContext(
		ctx,
		discovery.WebsocketURL,
		headers,
	)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}

		return false, err
	}
	defer conn.Close()

	if err := conn.WriteJSON(
		map[string]interface{}{
			"type": "connection_init",
			"payload": map[string]string{
				"token": t.token,
			},
		},
	); err != nil {
		return false, err
	}

	// Wait for connection_ack before subscribing.
	conn.SetReadDeadline(
		time.Now().Add(10 * time.Second),
	)

	for {
		var message tibberWSMessage

		if err := conn.ReadJSON(&message); err != nil {
			return false, err
		}

		switch message.Type {
		case "connection_ack":
			goto acknowledged

		case "ping":
			if err := t.sendPong(
				conn,
				message.Payload,
			); err != nil {
				return false, err
			}

		case "error":
			return false, fmt.Errorf(
				"Tibber websocket initialization error: %s",
				string(message.Payload),
			)
		}
	}

acknowledged:

	subscription := `
subscription LiveMeasurement($homeId: ID!) {
  liveMeasurement(homeId: $homeId) {
    timestamp
    power
    powerProduction
    accumulatedConsumptionLastHour
    accumulatedProductionLastHour
  }
}`

	err = conn.WriteJSON(
		map[string]interface{}{
			"id":   "live",
			"type": "subscribe",
			"payload": map[string]interface{}{
				"query": subscription,
				"variables": map[string]string{
					"homeId": discovery.Home.ID,
				},
			},
		},
	)
	if err != nil {
		return false, err
	}

	t.setConnected(
		discovery.Home.ID,
		discovery.Home.AppNickname,
	)

	log.Printf(
		"Tibber connected: home=%q id=%s",
		discovery.Home.AppNickname,
		discovery.Home.ID,
	)

	hadData := false

	for {
		timeout :=
			time.Duration(cfg.TibberTimeoutSeconds) *
				time.Second

		conn.SetReadDeadline(
			time.Now().Add(timeout),
		)

		var message tibberWSMessage

		if err := conn.ReadJSON(&message); err != nil {
			return hadData, err
		}

		switch message.Type {
		case "next":
			var payload struct {
				Data struct {
					LiveMeasurement *tibberLiveMeasurement `json:"liveMeasurement"`
				} `json:"data"`
			}

			if err := json.Unmarshal(
				message.Payload,
				&payload,
			); err != nil {
				return hadData, err
			}

			if payload.Data.LiveMeasurement != nil {
				t.setMeasurement(
					*payload.Data.LiveMeasurement,
				)

				hadData = true
			}

		case "ping":
			if err := t.sendPong(
				conn,
				message.Payload,
			); err != nil {
				return hadData, err
			}

		case "error":
			return hadData, fmt.Errorf(
				"Tibber subscription error: %s",
				string(message.Payload),
			)

		case "complete":
			return hadData, fmt.Errorf(
				"Tibber subscription completed",
			)
		}
	}
}
