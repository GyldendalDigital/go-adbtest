package ui

import (
	"testing"
)

const testDump = `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
  <node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.example.app" content-desc="" clickable="false" bounds="[0,0][1080,1920]">
    <node index="0" text="Allow" resource-id="com.android.permissioncontroller:id/permission_allow_button" class="android.widget.Button" package="com.android.permissioncontroller" content-desc="" clickable="true" bounds="[100,800][500,900]">
    </node>
    <node index="1" text="Deny" resource-id="com.android.permissioncontroller:id/permission_deny_button" class="android.widget.Button" package="com.android.permissioncontroller" content-desc="" clickable="true" bounds="[550,800][980,900]">
    </node>
    <node index="2" text="While using the app" resource-id="com.android.permissioncontroller:id/permission_allow_foreground_only_button" class="android.widget.Button" package="com.android.permissioncontroller" content-desc="" clickable="true" bounds="[100,1000][980,1100]">
    </node>
    <node index="3" text="Settings" resource-id="com.example.app:id/settings_label" class="android.widget.TextView" package="com.example.app" content-desc="Open settings" clickable="false" bounds="[0,200][540,300]">
    </node>
  </node>
</hierarchy>`

func TestParseDump(t *testing.T) {
	elements, err := ParseDump([]byte(testDump))
	if err != nil {
		t.Fatalf("ParseDump() error: %v", err)
	}
	// 1 root FrameLayout + 4 children = 5 elements
	if len(elements) != 5 {
		t.Fatalf("ParseDump() returned %d elements, want 5", len(elements))
	}
}

func TestParseDump_ElementFields(t *testing.T) {
	elements, err := ParseDump([]byte(testDump))
	if err != nil {
		t.Fatalf("ParseDump() error: %v", err)
	}

	// Find the "Allow" button (index 1 in flat list, after root)
	allow := elements[1]
	if allow.Text != "Allow" {
		t.Errorf("Text = %q, want %q", allow.Text, "Allow")
	}
	if allow.ResourceID != "com.android.permissioncontroller:id/permission_allow_button" {
		t.Errorf("ResourceID = %q", allow.ResourceID)
	}
	if allow.Class != "android.widget.Button" {
		t.Errorf("Class = %q", allow.Class)
	}
	if !allow.Clickable {
		t.Error("Clickable = false, want true")
	}
	if allow.Bounds.X1 != 100 || allow.Bounds.Y1 != 800 || allow.Bounds.X2 != 500 || allow.Bounds.Y2 != 900 {
		t.Errorf("Bounds = %+v, want {100 800 500 900}", allow.Bounds)
	}
}

func TestParseDump_ContentDesc(t *testing.T) {
	elements, err := ParseDump([]byte(testDump))
	if err != nil {
		t.Fatal(err)
	}

	settings := elements[4]
	if settings.ContentDesc != "Open settings" {
		t.Errorf("ContentDesc = %q, want %q", settings.ContentDesc, "Open settings")
	}
}

func TestParseDump_InvalidXML(t *testing.T) {
	_, err := ParseDump([]byte("not xml"))
	if err == nil {
		t.Fatal("expected error for invalid XML")
	}
}

func TestRect_Center(t *testing.T) {
	r := Rect{X1: 100, Y1: 200, X2: 300, Y2: 400}
	if r.CenterX() != 200 {
		t.Errorf("CenterX() = %d, want 200", r.CenterX())
	}
	if r.CenterY() != 300 {
		t.Errorf("CenterY() = %d, want 300", r.CenterY())
	}
}

func TestParseBounds(t *testing.T) {
	tests := []struct {
		input string
		want  Rect
		err   bool
	}{
		{"[0,0][1080,1920]", Rect{0, 0, 1080, 1920}, false},
		{"[100,200][300,400]", Rect{100, 200, 300, 400}, false},
		{"invalid", Rect{}, true},
		{"", Rect{}, true},
	}

	for _, tc := range tests {
		got, err := parseBounds(tc.input)
		if tc.err {
			if err == nil {
				t.Errorf("parseBounds(%q) = nil error, want error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBounds(%q) error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseBounds(%q) = %+v, want %+v", tc.input, got, tc.want)
		}
	}
}

func TestFindByText(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	found := FindByText(elements, "Allow")
	if len(found) != 1 {
		t.Fatalf("FindByText('Allow') = %d results, want 1", len(found))
	}
	if found[0].Text != "Allow" {
		t.Errorf("FindByText('Allow')[0].Text = %q", found[0].Text)
	}
}

func TestFindByText_Substring(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	found := FindByText(elements, "using the app")
	if len(found) != 1 {
		t.Fatalf("FindByText('using the app') = %d results, want 1", len(found))
	}
}

func TestFindByText_NoMatch(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	found := FindByText(elements, "nonexistent")
	if len(found) != 0 {
		t.Errorf("FindByText('nonexistent') = %d results, want 0", len(found))
	}
}

func TestFindByResourceID(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	found := FindByResourceID(elements, "com.example.app:id/settings_label")
	if len(found) != 1 {
		t.Fatalf("FindByResourceID() = %d results, want 1", len(found))
	}
	if found[0].Text != "Settings" {
		t.Errorf("found[0].Text = %q, want %q", found[0].Text, "Settings")
	}
}

func TestFindByResourceID_NoMatch(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	found := FindByResourceID(elements, "com.example.app:id/nonexistent")
	if len(found) != 0 {
		t.Errorf("FindByResourceID() = %d results, want 0", len(found))
	}
}

func TestFindByTextRegex(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	found, err := FindByTextRegex(elements, `^(Allow|Deny)$`)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("FindByTextRegex() = %d results, want 2", len(found))
	}
}

func TestFindByTextRegex_InvalidPattern(t *testing.T) {
	elements, _ := ParseDump([]byte(testDump))

	_, err := FindByTextRegex(elements, `[invalid`)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}
