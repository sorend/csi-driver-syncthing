package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Syncthing struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

type Device struct {
	DeviceID  string   `json:"deviceID"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Paused    bool     `json:"paused"`
}

type Folder struct {
	ID             string         `json:"id"`
	Label          string         `json:"label"`
	Path           string         `json:"path"`
	Type           string         `json:"type"`
	Devices        []FolderDevice `json:"devices"`
	Paused         bool           `json:"paused"`
	FilesystemType string         `json:"fsType,omitempty"`
}

type FolderDevice struct {
	DeviceID string `json:"deviceID"`
}

type SystemStatus struct {
	MyID string `json:"myID"`
}

type Completion struct {
	Completion  float64 `json:"completion"`
	NeedBytes   int64   `json:"needBytes"`
	NeedItems   int64   `json:"needItems"`
	GlobalBytes int64   `json:"globalBytes"`
}

type FolderStatus struct {
	State string `json:"state"`
}

func (s *Syncthing) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Syncthing) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		content, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(content)
	}
	base := strings.TrimRight(s.BaseURL, "/")
	request, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("X-API-Key", s.APIKey)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		return fmt.Errorf("Syncthing API %s %s returned %s: %s", method, path, response.Status, strings.TrimSpace(string(content)))
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}

var ErrNotFound = fmt.Errorf("Syncthing object not found")

func (s *Syncthing) Status(ctx context.Context) (SystemStatus, error) {
	var status SystemStatus
	err := s.request(ctx, http.MethodGet, "/rest/system/status", nil, &status)
	return status, err
}

func (s *Syncthing) Version(ctx context.Context) (string, error) {
	var version struct {
		Version string `json:"version"`
	}
	err := s.request(ctx, http.MethodGet, "/rest/system/version", nil, &version)
	return version.Version, err
}

func (s *Syncthing) Config(ctx context.Context) (map[string]any, error) {
	var config map[string]any
	err := s.request(ctx, http.MethodGet, "/rest/config", nil, &config)
	return config, err
}

func (s *Syncthing) UpdateConfig(ctx context.Context, config map[string]any) error {
	return s.request(ctx, http.MethodPut, "/rest/config", config, nil)
}

func (s *Syncthing) Devices(ctx context.Context) ([]Device, error) {
	var devices []Device
	err := s.request(ctx, http.MethodGet, "/rest/config/devices", nil, &devices)
	return devices, err
}

func (s *Syncthing) Folders(ctx context.Context) ([]Folder, error) {
	var folders []Folder
	err := s.request(ctx, http.MethodGet, "/rest/config/folders", nil, &folders)
	return folders, err
}

func (s *Syncthing) EnsureDevice(ctx context.Context, device Device) error {
	var existing map[string]any
	err := s.request(ctx, http.MethodGet, "/rest/config/devices/"+url.PathEscape(device.DeviceID), nil, &existing)
	if err == nil {
		existing["deviceID"] = device.DeviceID
		existing["name"] = device.Name
		existing["addresses"] = device.Addresses
		return s.request(ctx, http.MethodPut, "/rest/config/devices/"+url.PathEscape(device.DeviceID), existing, nil)
	}
	if err != ErrNotFound {
		return err
	}
	return s.request(ctx, http.MethodPost, "/rest/config/devices", device, nil)
}

func (s *Syncthing) EnsureFolder(ctx context.Context, folder Folder) error {
	var existing map[string]any
	err := s.request(ctx, http.MethodGet, "/rest/config/folders/"+url.PathEscape(folder.ID), nil, &existing)
	if err == nil {
		existing["id"] = folder.ID
		existing["label"] = folder.Label
		existing["path"] = folder.Path
		existing["type"] = folder.Type
		existing["devices"] = folder.Devices
		return s.request(ctx, http.MethodPut, "/rest/config/folders/"+url.PathEscape(folder.ID), existing, nil)
	}
	if err != ErrNotFound {
		return err
	}
	return s.request(ctx, http.MethodPost, "/rest/config/folders", folder, nil)
}

func (s *Syncthing) RemoveFolder(ctx context.Context, id string) error {
	err := s.request(ctx, http.MethodDelete, "/rest/config/folders/"+url.PathEscape(id), nil, nil)
	if err == ErrNotFound {
		return nil
	}
	return err
}

func (s *Syncthing) SetFolderPause(ctx context.Context, id string, paused bool) error {
	path := "/rest/config/folders/" + url.PathEscape(id)
	var folder map[string]any
	if err := s.request(ctx, http.MethodGet, path, nil, &folder); err != nil {
		return err
	}
	folder["paused"] = paused
	return s.request(ctx, http.MethodPut, path, folder, nil)
}

func (s *Syncthing) RemoveDevice(ctx context.Context, id string) error {
	err := s.request(ctx, http.MethodDelete, "/rest/config/devices/"+url.PathEscape(id), nil, nil)
	if err == ErrNotFound {
		return nil
	}
	return err
}

func (s *Syncthing) Completion(ctx context.Context, folderID string) (Completion, error) {
	var completion Completion
	err := s.request(ctx, http.MethodGet, "/rest/db/completion?folder="+url.QueryEscape(folderID), nil, &completion)
	return completion, err
}

func (s *Syncthing) FolderStatus(ctx context.Context, folderID string) (FolderStatus, error) {
	var status FolderStatus
	err := s.request(ctx, http.MethodGet, "/rest/db/status?folder="+url.QueryEscape(folderID), nil, &status)
	return status, err
}

func (s *Syncthing) ConfiguredAddresses(ctx context.Context, id string) ([]string, error) {
	devices, err := s.Devices(ctx)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		if device.DeviceID == id {
			return device.Addresses, nil
		}
	}
	return nil, nil
}

func ParseCompletion(completion Completion) int32 {
	percentage := completion.Completion
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}
	return int32(percentage)
}

func completionPercent(raw json.Number) int32 {
	value, _ := strconv.ParseFloat(raw.String(), 64)
	return int32(value)
}
