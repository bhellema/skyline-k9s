// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"k8s.io/apimachinery/pkg/util/cache"
)

const (
	k9sGitURL          = "https://api.github.com/repos/derailed/k9s/releases/latest"
	cacheSize          = 10
	cacheExpiry        = 1 * time.Hour
	k9sLatestRevKey    = "k9sRev"
	skyInspectTimeout  = 5 * time.Second // Reduced from 8s since it's now async
	skyInspectBin      = "sky"
	skyInspectResource = "cm"
)

// ClusterInfoListener registers a listener for model changes.
type ClusterInfoListener interface {
	// ClusterInfoChanged notifies the cluster meta was changed.
	ClusterInfoChanged(prev, curr *ClusterMeta)

	// ClusterInfoUpdated notifies the cluster meta was updated.
	ClusterInfoUpdated(*ClusterMeta)
}

// ClusterMeta represents cluster meta data.
type ClusterMeta struct {
	Context, Cluster    string
	User                string
	Program             string
	EnvironmentType     string
	ReleaseID           string
	Sandbox             string
	K9sVer, K9sLatest   string
	K8sVer              string
	Cpu, Mem, Ephemeral int
}

// NewClusterMeta returns a new instance.
func NewClusterMeta() *ClusterMeta {
	return &ClusterMeta{
		Context:         client.NA,
		Cluster:         client.NA,
		User:            client.NA,
		Program:         client.NA,
		EnvironmentType: client.NA,
		ReleaseID:       client.NA,
		Sandbox:         client.NA,
		K9sVer:          client.NA,
		K8sVer:          client.NA,
		Cpu:             0,
		Mem:             0,
		Ephemeral:       0,
	}
}

// Deltas diffs cluster meta return true if different, false otherwise.
func (c *ClusterMeta) Deltas(n *ClusterMeta) bool {
	if c.Cpu != n.Cpu || c.Mem != n.Mem || c.Ephemeral != n.Ephemeral {
		return true
	}

	return c.Context != n.Context ||
		c.Cluster != n.Cluster ||
		c.User != n.User ||
		c.Program != n.Program ||
		c.EnvironmentType != n.EnvironmentType ||
		c.ReleaseID != n.ReleaseID ||
		c.Sandbox != n.Sandbox ||
		c.K8sVer != n.K8sVer ||
		c.K9sVer != n.K9sVer ||
		c.K9sLatest != n.K9sLatest
}

// ClusterInfo models cluster metadata.
type ClusterInfo struct {
	cluster   *Cluster
	factory   dao.Factory
	data      *ClusterMeta
	version   string
	cfg       *config.K9s
	listeners []ClusterInfoListener
	cache     *cache.LRUExpireCache
	mx        sync.RWMutex
	skyInfo   *skyCustomerInfo
}

// NewClusterInfo returns a new instance.
func NewClusterInfo(f dao.Factory, v string, cfg *config.K9s) *ClusterInfo {
	c := ClusterInfo{
		factory: f,
		cluster: NewCluster(f),
		data:    NewClusterMeta(),
		version: v,
		cfg:     cfg,
		cache:   cache.NewLRUExpireCache(cacheSize),
	}

	return &c
}

func (c *ClusterInfo) fetchK9sLatestRev() string {
	rev, ok := c.cache.Get(k9sLatestRevKey)
	if ok {
		return rev.(string)
	}

	latestRev, err := fetchLatestRev()
	if err != nil {
		slog.Warn("k9s latest rev fetch failed", slogs.Error, err)
	} else {
		c.cache.Add(k9sLatestRevKey, latestRev, cacheExpiry)
	}

	return latestRev
}

// Reset resets context and reload.
func (c *ClusterInfo) Reset(f dao.Factory) {
	if f == nil {
		return
	}

	c.mx.Lock()
	c.cluster, c.data = NewCluster(f), NewClusterMeta()
	c.skyInfo = nil
	c.mx.Unlock()

	c.Refresh()
}

// Refresh fetches the latest cluster meta.
func (c *ClusterInfo) Refresh() {
	data := NewClusterMeta()
	if c.factory.Client().ConnectionOK() {
		data.Context = c.cluster.ContextName()
		data.Cluster = c.cluster.ClusterName()
		data.User = c.cluster.UserName()
		data.K8sVer = c.cluster.Version()
		ctx, cancel := context.WithTimeout(context.Background(), c.cluster.factory.Client().Config().CallTimeout())
		defer cancel()
		var mx client.ClusterMetrics
		if err := c.cluster.Metrics(ctx, &mx); err == nil {
			data.Cpu, data.Mem, data.Ephemeral = mx.PercCPU, mx.PercMEM, mx.PercEphemeral
		}
	}

	// Fetch sky customer info asynchronously to avoid blocking UI
	go c.fetchSkyCustomerInfoAsync()

	// Use cached sky info if available (from previous fetch)
	if sky := c.getSkyInfoFromCache(); sky != nil {
		data.Program = sky.Program
		data.EnvironmentType = sky.EnvironmentType
		data.ReleaseID = sky.ReleaseID
		data.Sandbox = sky.Sandbox
	}

	data.K9sVer = c.version
	v1 := NewSemVer(data.K9sVer)

	var latestRev string
	if !c.cfg.SkipLatestRevCheck {
		latestRev = c.fetchK9sLatestRev()
	}
	v2 := NewSemVer(latestRev)

	data.K9sVer, data.K9sLatest = v1.String(), v2.String()
	if v1.IsCurrent(v2) {
		data.K9sLatest = ""
	}

	if c.data.Deltas(data) {
		c.fireMetaChanged(c.data, data)
	} else {
		c.fireNoMetaChanged(data)
	}
	c.mx.Lock()
	c.data = data
	c.mx.Unlock()
}

// AddListener adds a new model listener.
func (c *ClusterInfo) AddListener(l ClusterInfoListener) {
	c.listeners = append(c.listeners, l)
}

// RemoveListener delete a listener from the list.
func (c *ClusterInfo) RemoveListener(l ClusterInfoListener) {
	victim := -1
	for i, lis := range c.listeners {
		if lis == l {
			victim = i
			break
		}
	}

	if victim >= 0 {
		c.listeners = append(c.listeners[:victim], c.listeners[victim+1:]...)
	}
}

func (c *ClusterInfo) fireMetaChanged(prev, cur *ClusterMeta) {
	for _, l := range c.listeners {
		l.ClusterInfoChanged(prev, cur)
	}
}

func (c *ClusterInfo) fireNoMetaChanged(data *ClusterMeta) {
	for _, l := range c.listeners {
		l.ClusterInfoUpdated(data)
	}
}

// fetchSkyCustomerInfoAsync fetches sky customer info in the background.
// This prevents blocking the UI during startup.
func (c *ClusterInfo) fetchSkyCustomerInfoAsync() {
	defer func(t time.Time) {
		slog.Debug("Sky customer info fetch time", slogs.Elapsed, time.Since(t))
	}(time.Now())

	info, err := fetchSkyCustomerInfo()
	if err != nil {
		slog.Debug("Sky customer info unavailable", slogs.Error, err)
		return
	}

	c.mx.Lock()
	c.skyInfo = info
	c.mx.Unlock()

	// Update cluster data with sky info
	c.updateWithSkyInfo(info)
}

// getSkyInfoFromCache returns cached sky info without fetching.
func (c *ClusterInfo) getSkyInfoFromCache() *skyCustomerInfo {
	c.mx.RLock()
	defer c.mx.RUnlock()
	return c.skyInfo
}

// updateWithSkyInfo updates the cluster metadata with sky customer info.
func (c *ClusterInfo) updateWithSkyInfo(sky *skyCustomerInfo) {
	c.mx.Lock()
	if c.data != nil {
		c.data.Program = sky.Program
		c.data.EnvironmentType = sky.EnvironmentType
		c.data.ReleaseID = sky.ReleaseID
		c.data.Sandbox = sky.Sandbox
	}
	data := c.data
	c.mx.Unlock()

	// Notify listeners that data was updated
	if data != nil {
		c.fireNoMetaChanged(data)
	}
}

// Helpers...

func fetchLatestRev() (string, error) {
	slog.Debug("Fetching latest k9s rev...")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k9sGitURL, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	m := make(map[string]any, 20)
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}

	if v, ok := m["name"]; ok {
		slog.Debug("K9s latest rev", slogs.Revision, v.(string))
		return v.(string), nil
	}

	return "", errors.New("no version found")
}

type skyCustomerInfo struct {
	Program         string
	EnvironmentType string
	ReleaseID       string
	Sandbox         string
}

func fetchSkyCustomerInfo() (*skyCustomerInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), skyInspectTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, skyInspectBin, "inspect", skyInspectResource)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	info := parseSkyCustomerInfo(out)
	if info == nil {
		return nil, errors.New("malformed sky inspect cm output")
	}

	return info, nil
}

func parseSkyCustomerInfo(out []byte) *skyCustomerInfo {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	values := make(map[string]string)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := normalizeSkyKey(parts[0])
		value := strings.TrimSpace(parts[1])
		if key != "" {
			values[key] = value
		}
	}

	if len(values) == 0 {
		return nil
	}

	return &skyCustomerInfo{
		Program:         values["program"],
		EnvironmentType: values["environmenttype"],
		ReleaseID:       values["releaseid"],
		Sandbox:         values["sandbox"],
	}
}

func normalizeSkyKey(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, " ", "")
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	return k
}
