package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/preset"
	"github.com/pw/llmgw/internal/quota"
)

// quotaReportsResponse is the body of GET /admin/api/quota.
type quotaReportsResponse struct {
	Reports []quota.Report `json:"reports"`
}

// adminQuota answers the subscription quota of every upstream instance.
//
// The vendor quota endpoints are asked concurrently. One failing instance
// only fills in its own error, the others still report their windows.
func (s *Server) adminQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, quotaReportsResponse{Reports: s.quotaReports(r)})
}

// quotaReports asks the vendor quota endpoints for every instance.
func (s *Server) quotaReports(r *http.Request) []quota.Report {
	targets, err := s.quotaTargets()
	if err != nil {
		s.log.Error("read instances for quota", "method", r.Method, "path", r.URL.Path,
			"request_id", requestID(r), "err", err)
		return []quota.Report{}
	}
	reports := make([]quota.Report, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target quota.Target) {
			defer wg.Done()
			reports[i] = s.quota.Report(r.Context(), target)
		}(i, target)
	}
	wg.Wait()
	return reports
}

// StartQuotaRefresh periodically refreshes subscription quota reports for all
// built-in instances that expose a quota endpoint, keeping the router's
// in-memory quota cache warm even when nobody has the console open.
func (s *Server) StartQuotaRefresh(ctx context.Context) {
	s.refreshQuotaOnce(ctx)
	ticker := time.NewTicker(quota.DefaultTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshQuotaOnce(ctx)
		}
	}
}

func (s *Server) refreshQuotaOnce(parent context.Context) {
	targets, err := s.quotaTargets()
	if err != nil {
		s.log.Warn("background quota refresh: list instances failed", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, target := range targets {
		pr, ok := preset.Lookup(target.PresetID)
		if !ok || pr.Quota == nil {
			continue
		}
		wg.Add(1)
		go func(t quota.Target) {
			defer wg.Done()
			_ = s.quota.Report(ctx, t)
		}(target)
	}
	wg.Wait()
}

// quotaTargets lists the instances together with their plaintext upstream
// key. The key never leaves the server: it is only used for the quota query.
func (s *Server) quotaTargets() ([]quota.Target, error) {
	records, err := s.store.ListInstanceRecords()
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	targets := make([]quota.Target, 0, len(records))
	for _, rec := range records {
		target := quota.Target{ID: rec.ID, Name: rec.Name, PresetID: rec.Preset}
		if rec.EncryptedKey != "" {
			key, err := s.box.Decrypt(rec.EncryptedKey)
			if err != nil {
				target.Err = "无法解密上游 API Key"
			} else {
				target.APIKey = key
			}
		}
		if rec.EncryptedQuotaCredential != "" {
			credential, err := s.decryptQuotaCredential(rec.EncryptedQuotaCredential)
			if err != nil {
				target.Err = "无法解密额度查询凭证"
			} else {
				target.Credential = credential
			}
		}
		if rec.EncryptedProxyURL != "" {
			proxyURL, err := s.box.Decrypt(rec.EncryptedProxyURL)
			if err != nil {
				// The console only sees a generic message, so the real
				// reason has to reach the server log.
				s.log.Error("decrypt proxy url", "instance_id", rec.ID, "instance", rec.Name, "err", err)
				target.Err = "无法解密代理配置"
			} else {
				target.ProxyURL = proxyURL
			}
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// decryptQuotaCredential reads the quota credential of an instance back.
func (s *Server) decryptQuotaCredential(encrypted string) (quota.Credential, error) {
	raw, err := s.box.Decrypt(encrypted)
	if err != nil {
		return quota.Credential{}, fmt.Errorf("decrypt quota credential: %w", err)
	}
	var cred config.QuotaCredential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return quota.Credential{}, fmt.Errorf("decode quota credential: %w", err)
	}
	return quota.Credential{
		AccessKeyID:     cred.AccessKeyID,
		SecretAccessKey: cred.SecretAccessKey,
	}, nil
}
