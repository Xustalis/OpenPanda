// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
)

// reviewedFiles contains only audited text resources. Updating it requires a
// pinned revision, retained license and reviewed hashes in catalog.json.
//
//go:embed reviewed/*/* reviewed/catalog.json
var reviewedFiles embed.FS

func reviewedSkills() []HubSkill {
	var catalog []struct {
		HubSkill
		Files map[string]string `json:"files"`
	}
	data, err := reviewedFiles.ReadFile("reviewed/catalog.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		panic(err)
	}
	out := make([]HubSkill, 0, len(catalog))
	for _, entry := range catalog {
		files := bundleFiles{}
		for name, digest := range entry.Files {
			if path.Base(name) != name {
				panic("skills: invalid reviewed resource")
			}
			data, err := reviewedFiles.ReadFile("reviewed/" + entry.Name + "/" + name)
			if err != nil {
				panic(err)
			}
			if err := verifyDownload(data, digest); err != nil {
				panic(fmt.Sprintf("skills: reviewed %s/%s: %v", entry.Name, name, err))
			}
			files[name] = data
		}
		sk := entry.HubSkill
		sk.Content = string(files["SKILL.md"])
		sk.resources = files
		out = append(out, sk)
	}
	return out
}
