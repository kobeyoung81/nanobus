package skillbundle

import "testing"

func TestLoad_AllPackagesHaveSkillAndMetadata(t *testing.T) {
	skills, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"mdtopdf", "officecli", "officecli-docx", "officecli-pptx", "officecli-xlsx", "weather"}
	if len(skills) != len(want) {
		t.Fatalf("expected %d bundled skills, got %d", len(want), len(skills))
	}
	for i, name := range want {
		if skills[i].Name != name {
			t.Errorf("skill %d: got %q want %q", i, skills[i].Name, name)
		}
		if skills[i].Files["SKILL.md"] == "" {
			t.Errorf("%s: missing SKILL.md", name)
		}
		if skills[i].Description == "" {
			t.Errorf("%s: empty description", name)
		}
		if skills[i].Version < 1 {
			t.Errorf("%s: version %d < 1", name, skills[i].Version)
		}
		if len(skills[i].ContentHash) != 64 {
			t.Errorf("%s: content hash %q is not sha256 hex", name, skills[i].ContentHash)
		}
	}
}

func TestContentHash_OrderIndependent(t *testing.T) {
	a := map[string]string{"SKILL.md": "body", "creating.md": "how"}
	b := map[string]string{"creating.md": "how", "SKILL.md": "body"}
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("content hash must not depend on map iteration order")
	}
	if ContentHash(a) == ContentHash(map[string]string{"SKILL.md": "other"}) {
		t.Fatal("content hash must change when content changes")
	}
}

func TestFrontmatterValue(t *testing.T) {
	content := "---\nname: weather\ndescription: Get the weather\nmetadata: {\"nanobot\":{}}\n---\n\n# Body\n"
	if got := frontmatterValue(content, "description"); got != "Get the weather" {
		t.Fatalf("description = %q", got)
	}
	if got := frontmatterValue("# no frontmatter\ndescription: nope\n", "description"); got != "" {
		t.Fatalf("expected empty for missing frontmatter, got %q", got)
	}
}
