package slug

import "testing"

func TestMake(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Động vật quanh bé", "dong-vat-quanh-be"},
		{"Thế giới thực vật", "the-gioi-thuc-vat"},
		{"Bác Hồ với thiếu nhi", "bac-ho-voi-thieu-nhi"},
		{"  Spaces   and -- dashes  ", "spaces-and-dashes"},
		{"123 số", "123-so"},
		{"Gói: Kỹ năng sống!", "goi-ky-nang-song"},
	} {
		if got := Make(tc.in); got != tc.want {
			t.Errorf("Make(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, valid := range []string{"a", "abc-def", "abc-123"} {
		if !Valid(valid) {
			t.Errorf("Valid(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"", "Abc", "-abc", "abc-", "abc--def", "abc def", "abc_def"} {
		if Valid(invalid) {
			t.Errorf("Valid(%q) = true", invalid)
		}
	}
}
