package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// ClawHubRegistry implements SkillRegistry for ClawHub (official registry)
type ClawHubRegistry struct {
	client  *http.Client
	baseURL string
	token   string
}

// NewClawHubRegistry creates a new ClawHub registry
func NewClawHubRegistry() *ClawHubRegistry {
	return &ClawHubRegistry{
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: "https://clawhub.ai",
	}
}

// Name returns the registry name
func (r *ClawHubRegistry) Name() string {
	return "clawhub"
}

// Search searches ClawHub for skills.
//
// An empty query is a browse request: /api/v1/search?q= returns zero rows, so
// browsing goes to /api/v1/skills instead. This used to return a hardcoded
// eight-skill "featured" list, which showed the same stale skills to everyone
// regardless of what the registry actually holds.
func (r *ClawHubRegistry) Search(ctx context.Context, query string, limit int) ([]HubSkill, error) {
	if limit <= 0 {
		limit = 10
	}

	if query == "" {
		return r.browse(ctx, limit)
	}

	apiURL := fmt.Sprintf("%s/api/v1/search?q=%s&limit=%d", r.baseURL, url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "go-magic-skill-hub")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		fmt.Printf("ClawHub search failed: %v\n", err)
		return []HubSkill{}, fmt.Errorf("clawhub search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("ClawHub API returned %d\n", resp.StatusCode)
		return []HubSkill{}, fmt.Errorf("clawhub API returned %d", resp.StatusCode)
	}

	// ClawHub search API returns: {"results": [{"score", "slug", "displayName", "summary", "version", "updatedAt", "ownerHandle", "owner": {...}}]}
	var result struct {
		Results []struct {
			Score       float64 `json:"score"`
			Slug        string  `json:"slug"`
			DisplayName string  `json:"displayName"`
			Summary     string  `json:"summary"`
			Version     *string `json:"version"`
			UpdatedAt   int64   `json:"updatedAt"`
			OwnerHandle string  `json:"ownerHandle"`
			Owner       struct {
				Handle      string `json:"handle"`
				DisplayName string `json:"displayName"`
				Image       string `json:"image"`
			} `json:"owner"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return []HubSkill{}, fmt.Errorf("clawhub decode failed: %w", err)
	}

	// No fallback to a canned list: an empty result set must surface as empty so
	// the UI can say "no match" instead of showing unrelated skills that look
	// like search hits. Star/install counts are not part of the search response,
	// so they stay zero here and are filled in by GetSkillMeta when the user
	// opens a skill (the previous version looked them up in the hardcoded set,
	// which only ever worked for those eight slugs).
	var skills []HubSkill
	for _, s := range result.Results {
		skills = append(skills, HubSkill{
			Name:        s.DisplayName,
			Description: s.Summary,
			Source:      HubSourceHub,
			SourceID:    s.Slug,
			URL:         fmt.Sprintf("%s/skills/%s", r.baseURL, s.Slug),
			Verified:    true,
		})
	}

	return skills, nil
}

// browse lists what the registry actually holds, used when the user opens the
// hub without typing a keyword. GET /api/v1/skills returns
// {"items": [{"slug", "displayName", "summary", "tags":{"latest":...},
// "stats":{"downloads","stars",...}}], "nextCursor": ...}
func (r *ClawHubRegistry) browse(ctx context.Context, limit int) ([]HubSkill, error) {
	apiURL := fmt.Sprintf("%s/api/v1/skills?limit=%d", r.baseURL, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "go-magic-skill-hub")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		fmt.Printf("ClawHub browse failed: %v\n", err)
		return []HubSkill{}, fmt.Errorf("clawhub browse failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("ClawHub browse API returned %d\n", resp.StatusCode)
		return []HubSkill{}, fmt.Errorf("clawhub browse API returned %d", resp.StatusCode)
	}

	var result struct {
		Items []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"displayName"`
			Summary     string `json:"summary"`
			OwnerHandle string `json:"ownerHandle"`
			Tags        struct {
				Latest string `json:"latest"`
			} `json:"tags"`
			Stats struct {
				Downloads int `json:"downloads"`
				Stars     int `json:"stars"`
			} `json:"stats"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return []HubSkill{}, fmt.Errorf("clawhub browse decode failed: %w", err)
	}

	var skills []HubSkill
	for _, s := range result.Items {
		if s.Slug == "" {
			continue // 没有 slug 就没法安装，别往列表里塞装不上的条目
		}
		var tags []string
		if s.Tags.Latest != "" {
			tags = []string{s.Tags.Latest}
		}
		skills = append(skills, HubSkill{
			Name:        s.DisplayName,
			Description: s.Summary,
			Tags:        tags,
			Author:      s.OwnerHandle,
			Source:      HubSourceHub,
			SourceID:    s.Slug,
			URL:         fmt.Sprintf("%s/skills/%s", r.baseURL, s.Slug),
			Verified:    true,
			Stars:       s.Stats.Stars,
			Installs:    s.Stats.Downloads,
		})
	}

	return skills, nil
}

// GetSkillMeta gets metadata for a specific skill
func (r *ClawHubRegistry) GetSkillMeta(ctx context.Context, slug string) (*HubSkill, error) {
	apiURL := fmt.Sprintf("%s/api/v1/skills/%s", r.baseURL, url.QueryEscape(slug))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "go-magic-skill-hub")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clawhub API returned %d", resp.StatusCode)
	}

	// ClawHub API returns nested structure:
	// {"skill": {"slug", "displayName", "summary", "tags", "stats": {"stars", "downloads", ...}}, "latestVersion": {...}, "owner": {...}}
	var result struct {
		Skill struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"displayName"`
			Summary     string `json:"summary"`
			Tags        struct {
				Latest string `json:"latest"`
			} `json:"tags"`
			Stats struct {
				Stars           int `json:"stars"`
				Downloads       int `json:"downloads"`
				InstallsAllTime int `json:"installsAllTime"`
			} `json:"stats"`
			Verified bool `json:"verified"`
		} `json:"skill"`
		LatestVersion struct {
			Version string `json:"version"`
		} `json:"latestVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	s := result.Skill
	return &HubSkill{
		Name:        s.DisplayName,
		Description: s.Summary,
		Tags:        []string{s.Tags.Latest},
		Source:      HubSourceHub,
		SourceID:    s.Slug,
		URL:         fmt.Sprintf("%s/skills/%s", r.baseURL, s.Slug),
		Stars:       s.Stats.Stars,
		Installs:    s.Stats.InstallsAllTime,
		Verified:    s.Verified,
	}, nil
}

// DownloadAndInstall downloads and installs a skill from ClawHub.
//
// Every failure path returns an error. It used to fall back to writing a stub
// SKILL.md built from a hardcoded featured list, which made a failed install
// report success and left the user with a skill whose entire body was a copy of
// its own description — the skill would load, match on its name, and then do
// nothing useful. Failing loudly is the honest outcome.
func (r *ClawHubRegistry) DownloadAndInstall(ctx context.Context, slug, version, targetDir string) error {
	// ClawHub download API: /api/v1/download?slug={slug}&version={version}
	apiURL := fmt.Sprintf("%s/api/v1/download?slug=%s", r.baseURL, url.QueryEscape(slug))
	if version != "" {
		apiURL += "&version=" + url.QueryEscape(version)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "go-magic-skill-hub")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("clawhub download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clawhub download returned %d", resp.StatusCode)
	}

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "clawhub-skill-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	// Extract zip (使用包内安全版本 extractZipFile，防止路径穿越)
	if err := extractZipFile(tmpFile.Name(), targetDir); err != nil {
		return fmt.Errorf("failed to extract skill %s: %w", slug, err)
	}

	// Flatten zip structure if there's a single top-level directory
	if err := flattenZipStructure(targetDir); err != nil {
		// Non-fatal: continue with original structure
	}

	return nil
}

// flattenZipStructure moves files from a single top-level subdirectory up to targetDir
func flattenZipStructure(targetDir string) error {
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return err
	}

	// Find single top-level directory
	var subDir string
	for _, e := range entries {
		if e.IsDir() {
			if subDir != "" {
				// More than one directory, don't flatten
				return nil
			}
			subDir = e.Name()
		} else {
			// Files exist at top level, don't flatten
			return nil
		}
	}

	if subDir == "" {
		return nil
	}

	subDirPath := filepath.Join(targetDir, subDir)
	subEntries, err := os.ReadDir(subDirPath)
	if err != nil {
		return err
	}

	// Move all files from subDir to targetDir
	for _, e := range subEntries {
		src := filepath.Join(subDirPath, e.Name())
		dst := filepath.Join(targetDir, e.Name())
		if err := os.Rename(src, dst); err != nil {
			// If rename fails (cross-device), copy instead
			if e.IsDir() {
				if err := copyDirectory(src, dst); err != nil {
					return err
				}
				os.RemoveAll(src)
			} else {
				data, err := os.ReadFile(src)
				if err != nil {
					return err
				}
				if err := os.WriteFile(dst, data, 0644); err != nil {
					return err
				}
				os.Remove(src)
			}
		}
	}

	// Remove empty subDir
	os.Remove(subDirPath)
	return nil
}
