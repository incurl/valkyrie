package jobs

import (
	"encoding/base64"
	"fmt"
)

// ExportedArtifact is a stored file prepared for download.
//
// It carries the format of the bytes, so that the saved file is named
// and typed after the format it really is, instead of always being
// called ".png".
type ExportedArtifact struct {
	// Bytes is the base64 encoded content.
	Bytes string `json:"bytes"`
	// Extension is the file extension, without the dot.
	Extension string `json:"extension"`
	// Mime is the MIME type of the bytes.
	Mime string `json:"mime"`
	// Name is the suggested file name.
	Name string `json:"name"`
}

// ExportArtifact returns the bytes of a stored artifact.
//
// The format is detected from the bytes themselves, so the downloaded
// file is named after the format the content really is.
func (m *Manager) ExportArtifact(artifactID string) (*ExportedArtifact, error) {
	record, err := m.app.FindRecordById(CollectionArtifacts, artifactID)
	if err != nil {
		return nil, fmt.Errorf("find artifact %q: %w", artifactID, err)
	}

	data, err := readFileBytes(m.app, record)
	if err != nil {
		return nil, err
	}

	media := DetectMediaType(data)
	name := record.GetString("name")
	if name == "" {
		name = "artifact." + media.Extension
	}

	return &ExportedArtifact{
		Bytes:     base64.StdEncoding.EncodeToString(data),
		Extension: media.Extension,
		Mime:      media.Mime,
		Name:      name,
	}, nil
}
