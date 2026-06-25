package azure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// manifestVersion is bumped when the on-disk shape changes incompatibly.
const manifestVersion = "1"

// manifestFile is the index of an azure_export/ folder — the typed contract
// that flows from `snapshot` to `transform`. Everything else in the folder is
// plain source / JSONL the manifest points at; the manifest itself is the only
// thing transform parses.
const manifestFile = "manifest.json"

// Manifest indexes one snapshot.
type Manifest struct {
	Version       string               `json:"version"`
	CreatedAt     string               `json:"created_at"`
	Subscription  string               `json:"subscription"`
	ResourceGroup string               `json:"resource_group"`
	SliceName     string               `json:"slice_name"`
	Functions     []ManifestFunction   `json:"functions"`
	Collections   []ManifestCollection `json:"collections"`
	Blobs         []ManifestBlob       `json:"blobs"`
	Queues        []ManifestQueue      `json:"queues"`
	Secrets       []ManifestSecret     `json:"secrets"`
	Sites         []ManifestSite       `json:"sites"`
	Refusals      []Refusal            `json:"refusals"`
}

// ManifestTrigger is the one trigger a function declares.
type ManifestTrigger struct {
	Type     string `json:"type"`               // http | queue | timer
	Method   string `json:"method,omitempty"`   // http: get/post/put/delete/patch
	Route    string `json:"route,omitempty"`    // http route (Drift form)
	Queue    string `json:"queue,omitempty"`    // queue: source queue name
	Schedule string `json:"schedule,omitempty"` // timer: 5-field cron (NCRONTAB-translated)
	Auth     string `json:"auth"`               // none | apikey
}

// ManifestFunction is one movable Azure function, ready to scaffold.
type ManifestFunction struct {
	Name       string          `json:"name"`    // azure function name
	Handler    string          `json:"handler"` // Drift handler name (language casing)
	Runtime    string          `json:"runtime"` // python | node
	Trigger    ManifestTrigger `json:"trigger"`
	SourcePath string          `json:"source_path"` // relative path to the original source file
	SHA256     string          `json:"sha256"`
	Secrets    []string        `json:"secrets,omitempty"`  // app-setting names the function reads
	Bindings   []string        `json:"bindings,omitempty"` // non-trigger bindings → REPORT.md TODOs
}

// ManifestCollection is one Cosmos (Mongo API) collection exported as JSONL.
type ManifestCollection struct {
	Name     string `json:"name"`      // Drift collection name
	Account  string `json:"account"`   // azure cosmos account
	DataPath string `json:"data_path"` // relative path to the JSONL export
	DocCount int    `json:"doc_count"`
	SHA256   string `json:"sha256"`
}

// ManifestBlob is one blob container captured under DataDir (its blobs written
// as files, ready to seed Drift Backbone Blobs with the container as the bucket).
type ManifestBlob struct {
	Account   string `json:"account"`
	Container string `json:"container"`
	DataDir   string `json:"data_dir"` // relative dir holding the blobs
	BlobCount int    `json:"blob_count"`
	Bytes     int64  `json:"bytes"`
}

// ManifestQueue is one storage queue. Messages are PEEKED, never received — the
// source queue is left intact; they are re-pushed at apply, never seeded.
type ManifestQueue struct {
	Account      string `json:"account"`
	Name         string `json:"name"`
	DataPath     string `json:"data_path"` // relative path to the peeked-messages JSONL
	MessageCount int    `json:"message_count"`
}

// ManifestSecret is one app setting captured as a Drift secret. Value is only
// present when the operator passed --deref-secrets.
type ManifestSecret struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// ManifestSite is one Static Web App captured for Canvas.
type ManifestSite struct {
	Name      string `json:"name"`
	SourceDir string `json:"source_dir"` // relative dir in azure_export/
}

var (
	camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	slugRe  = regexp.MustCompile(`[^a-z0-9-]+`)
)

// slugify reduces a string to ^[a-z0-9-]+$ (Drift's name grammar): camelCase
// boundaries become dashes ("GetOrder" → "get-order"), then lowercase, strip,
// collapse, trim.
func slugify(s string) string {
	s = camelRe.ReplaceAllString(strings.TrimSpace(s), "$1-$2")
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	// Clamp to Drift's name grammar (1–32 chars, no trailing hyphen). A long
	// resource-group or resource name would otherwise produce a slice/collection
	// name that fails ParseDriftfile validation.
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		return "migrated"
	}
	return s
}

// slug is the slice name a deploy would use — explicit SliceName, else the
// resource group, slugified.
func (m Manifest) slug() string {
	if m.SliceName != "" {
		return slugify(m.SliceName)
	}
	return slugify(m.ResourceGroup)
}

// sha256hex is the hash recorded for every exported artifact, so transform can
// verify (and --resume can skip) what snapshot wrote.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeManifest(dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestFile), append(b, '\n'), 0o644)
}

func readManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}
