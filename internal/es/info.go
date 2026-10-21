package es

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// Info is what GET / says about a cluster.
type Info struct {
	Name, ClusterName, UUID string
	Version                 string // "7.10.2"
	Distribution            string // "elasticsearch" or "opensearch"
	Major, Minor            int
}

// OpenSearch reports an OpenSearch cluster.
func (i Info) OpenSearch() bool { return i.Distribution == "opensearch" }

// AtLeast reports a version of major.minor or later.
func (i Info) AtLeast(major, minor int) bool {
	return i.Major > major || i.Major == major && i.Minor >= minor
}

// GetInfo reads GET /.
func GetInfo(ctx context.Context, d Doer) (Info, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "/"})
	if err != nil {
		return Info{}, err
	}
	var r struct {
		Name        string `json:"name"`
		ClusterName string `json:"cluster_name"`
		UUID        string `json:"cluster_uuid"`
		Version     struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
		} `json:"version"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return Info{}, err
	}
	i := Info{Name: r.Name, ClusterName: r.ClusterName, UUID: r.UUID, Version: r.Version.Number, Distribution: r.Version.Distribution}
	if i.Distribution == "" {
		i.Distribution = "elasticsearch"
	}
	parts := strings.SplitN(i.Version, ".", 3)
	if len(parts) >= 2 {
		i.Major, _ = strconv.Atoi(parts[0])
		i.Minor, _ = strconv.Atoi(parts[1])
	}
	return i, nil
}
