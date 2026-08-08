package hub

import (
	"encoding/json"
	"errors"
	"fmt"
)

type ErrorKind int

const (
	KindOther ErrorKind = iota
	KindRateLimited
	KindUnauthorized
	KindGatedRepo
	KindNotFound
	KindNetwork
)

type Error struct {
	Op   string
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("hub %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("hub %s", e.Op)
}

func (e *Error) Unwrap() error { return e.Err }

var (
	ErrRateLimited  = &Error{Kind: KindRateLimited}
	ErrUnauthorized = &Error{Kind: KindUnauthorized}
	ErrGatedRepo    = &Error{Kind: KindGatedRepo}
	ErrNotFound     = &Error{Kind: KindNotFound}
	ErrNetwork      = &Error{Kind: KindNetwork}
)

func IsRateLimited(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindRateLimited
}

func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindUnauthorized
}

func IsGatedRepo(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindGatedRepo
}

func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindNotFound
}

type SearchParams struct {
	Query     string
	Filter    string
	Sort      string
	Direction string
	Limit     int
	Full      bool
}

func (p SearchParams) values() map[string]string {
	v := make(map[string]string)
	if p.Query != "" {
		v["search"] = p.Query
	}
	if p.Filter != "" {
		v["filter"] = p.Filter
	}
	if p.Sort != "" {
		v["sort"] = p.Sort
	}
	if p.Direction != "" {
		v["direction"] = p.Direction
	}
	if p.Limit > 0 {
		v["limit"] = fmt.Sprintf("%d", p.Limit)
	}
	if p.Full {
		v["full"] = "true"
	}
	return v
}

type ModelInfo struct {
	ID           string      `json:"id"`
	Author       string      `json:"author,omitempty"`
	SHA          string      `json:"sha,omitempty"`
	LastModified string      `json:"lastModified,omitempty"`
	Downloads    int         `json:"downloads,omitempty"`
	Likes        int         `json:"likes,omitempty"`
	PipelineTag  string      `json:"pipeline_tag,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	Private      bool        `json:"private"`
	Gated        gatedStatus `json:"gated"`
	Siblings     []FileEntry `json:"siblings,omitempty"`
	CardData     interface{} `json:"cardData,omitempty"`
}

type gatedStatus struct {
	IsGated bool
	Reason  string
}

func (g *gatedStatus) UnmarshalJSON(b []byte) error {
	var raw interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case bool:
		g.IsGated = v
	case string:
		g.IsGated = true
		g.Reason = v
	case map[string]interface{}:
		g.IsGated = true
	default:
		g.IsGated = false
	}
	return nil
}

type FileEntry struct {
	Path string   `json:"-"`
	Size int64    `json:"size"`
	LFS  *LFSInfo `json:"lfs,omitempty"`
	OID  string   `json:"oid,omitempty"`
}

func (f *FileEntry) UnmarshalJSON(b []byte) error {
	var raw struct {
		Rfilename string   `json:"rfilename"`
		Path      string   `json:"path"`
		Size      int64    `json:"size"`
		LFS       *LFSInfo `json:"lfs,omitempty"`
		OID       string   `json:"oid,omitempty"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	f.Path = raw.Rfilename
	if f.Path == "" {
		f.Path = raw.Path
	}
	f.Size = raw.Size
	f.LFS = raw.LFS
	f.OID = raw.OID
	return nil
}

type LFSInfo struct {
	SHA256      string `json:"sha256"`
	PointerSize int64  `json:"pointerSize"`
}
