package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormaliseBaseURL(t *testing.T) {
	cases := map[string]string{
		"lms.iba.edu.pk":                         "https://lms.iba.edu.pk",
		"https://lms.iba.edu.pk/":                "https://lms.iba.edu.pk",
		"https://lms.iba.edu.pk/portal/site/abc": "https://lms.iba.edu.pk",
		"  <https://lms.lums.edu.pk>  ":          "https://lms.lums.edu.pk",
		"":                                       "",
	}
	for in, want := range cases {
		if got := NormaliseBaseURL(in); got != want {
			t.Errorf("NormaliseBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigRoundTripWindowsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.BaseURL = "https://lms.iba.edu.pk"
	cfg.Username = "37103"
	cfg.Password = "s3cret"
	cfg.Destination = `D:\University\Courses`
	cfg.Courses = []Course{{ID: "abc-123", Folder: "Introduction to Programming (102033)"}}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if back.Destination != `D:\University\Courses` {
		t.Errorf("destination mangled: %q", back.Destination)
	}
	if back.Username != "37103" || back.Password != "s3cret" {
		t.Errorf("credentials mangled: %q / %q", back.Username, back.Password)
	}
	if len(back.Courses) != 1 || back.Courses[0].ID != "abc-123" {
		t.Errorf("courses mangled: %v", back.Courses)
	}
	if strings.Join(back.Sections, ",") != strings.Join(cfg.Sections, ",") {
		t.Errorf("sections mangled: %v, want %v", back.Sections, cfg.Sections)
	}
}

// The environment exists so a run need not keep a password on disk. Save
// wrote it there anyway, so the first sync through an MCP client — where
// the credentials arrive in the client's own config and nowhere else —
// left a second copy in a config.toml the student never created.
func TestAnEnvironmentPasswordIsNeverWrittenToTheConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.ApplyEnv("37103", "s3cret-from-env")
	cfg.Courses = []Course{{ID: "abc-123", Folder: "Calculus"}}

	if cfg.Password != "s3cret-from-env" {
		t.Fatalf("the environment did not reach the run: %q", cfg.Password)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "s3cret-from-env") {
		t.Error("the environment's password was written to config.toml")
	}
	// And it says where the credential actually comes from: a bare
	// `password = ''` reads as "none configured" on a setup that works.
	if !strings.Contains(string(body), "LMS_PASS") {
		t.Errorf("the config does not say where the password comes from:\n%s", body)
	}
	// The rest of the file still has to load — the line is a comment, and
	// the reader skips those.
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Courses) != 1 || back.Courses[0].Folder != "Calculus" {
		t.Errorf("the saved config no longer round-trips: %v", back.Courses)
	}
}

// The other half: overriding a stored password for one run must not erase
// it. A sync saves the config whenever it finds a new course, so a single
// run with LMS_PASS set would otherwise wipe the password out of a file the
// student had been using for a year.
func TestAStoredPasswordSurvivesAnEnvironmentOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.Username, cfg.Password = "37103", "from-the-file"
	cfg.ApplyEnv("", "from-the-env")

	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Password != "from-the-file" {
		t.Errorf("the stored password is %q, want it untouched", back.Password)
	}
}

// A password typed into the interface is the student telling us to keep it,
// whatever the environment says. Silently writing the old one back while
// reporting the settings saved is the worst of both.
func TestAPasswordTypedIntoTheInterfaceIsSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.ApplyEnv("37103", "from-the-env")
	cfg.SetPassword("typed-by-hand")

	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Password != "typed-by-hand" {
		t.Errorf("the typed password is %q, want it saved", back.Password)
	}
}

// A hand-edited config with absurd values must be clamped, not obeyed.
func TestConfigSanitise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("timeout = 0\nretries = 9999\ndelay = -5\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Timeout < 5 || cfg.Retries > 10 || cfg.Delay < 0 {
		t.Errorf("not clamped: timeout=%d retries=%d delay=%d",
			cfg.Timeout, cfg.Retries, cfg.Delay)
	}
}

// Junk in the config should be skipped, not fatal.
func TestConfigTolerLatesJunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("this is not toml\nusername = 'ok'\n!!!\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("junk should not be fatal: %v", err)
	}
	if cfg.Username != "ok" {
		t.Errorf("username = %q, want ok", cfg.Username)
	}
}

// A misspelt section would otherwise be obeyed and match nothing.
func TestUnknownSectionsAreDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// known_sections lists every tab, so no new one is offered and the
	// list is exactly what the file says, less what is not a tab.
	os.WriteFile(path, []byte("sections = ['resources', 'sylabus', 'nonsense']\n"+
		"known_sections = ["+sectionIDList()+"]\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Sections) != 1 || cfg.Sections[0] != "resources" {
		t.Errorf("sections = %v, want just [resources]", cfg.Sections)
	}

	// And a config that enables nothing valid must still sync something.
	os.WriteFile(path, []byte("sections = ['nonsense']\n"), 0o644)
	cfg, _ = LoadConfig(path)
	if len(cfg.sections()) == 0 {
		t.Error("a config with no valid section would sync nothing at all")
	}
}

func TestRelativeDestinationDoesNotDependOnTheWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(home, "config.toml")
	cfg.Destination = "Courses"

	got, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	// The test runs from the package directory, so a working-directory
	// resolution would land somewhere else entirely. An MCP client starts
	// this process from its own project folder and a scheduled run starts it
	// from $HOME — "Courses" has to mean one folder regardless.
	want := filepath.Join(home, "Courses")
	if got != want {
		t.Errorf("DestinationPath() = %s, want %s", got, want)
	}
}

func TestAbsoluteDestinationIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Destination = dir

	got, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(dir) {
		t.Errorf("DestinationPath() = %s, want %s", got, dir)
	}
}

func TestConfigRecordsWhetherItWasFound(t *testing.T) {
	dir := t.TempDir()
	missing, err := LoadConfig(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if missing.found {
		t.Error("a config that does not exist was reported as found")
	}

	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("destination = 'Courses'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !real.found {
		t.Error("a config that exists was reported as missing")
	}
}

// The written config's comment is the one place a person finds out a tab
// exists — an existing config carries an explicit sections line, so a newly
// supported tab never reaches it by default. That comment spelled out
// "resources, syllabus, dropbox" long after Overview, Announcements and
// Assignments were added, which is exactly how a real config sat there
// mirroring none of them.
func TestTheWrittenConfigNamesEveryTabThatCanBeEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Password = "x"
	if err := cfg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	body, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range sectionCatalogue() {
		if !strings.Contains(string(body), "'"+s.ID+"'") {
			t.Errorf("the config never mentions the %q tab:\n%s", s.ID, body)
		}
	}
	// And what it says is loadable: a comment listing an id that sanitise
	// drops would be worse than no comment.
	for _, s := range sectionCatalogue() {
		if !knownSections[s.ID] {
			t.Errorf("%q is offered but not a known section", s.ID)
		}
	}
}

// The zone is a setting because a phone cannot supply one: Termux runs this
// binary with no zone of its own, so every date would be UTC. A misspelt one
// falls back to the machine's rather than failing every run.
func TestTheTimezoneSettingIsReadSavedAndChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("timezone = 'Asia/Karachi'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.location().String() != "Asia/Karachi" {
		t.Errorf("zone = %s, want Asia/Karachi", cfg.location())
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if !strings.Contains(string(saved), "timezone    = 'Asia/Karachi'") {
		t.Errorf("the zone was not saved:\n%s", saved)
	}

	cfg.Timezone = "Asia/Karach"
	cfg.sanitise()
	if cfg.Timezone != "" || cfg.location() != time.Local {
		t.Errorf("a misspelt zone was kept: %q", cfg.Timezone)
	}
}

// The written config is the only place a student learns which tabs exist.
// Spelling the list out by hand is how it came to advertise three when the
// tool had grown to six.
func TestSavedConfigListsEveryAvailableSection(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(dir, "config.toml")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}

	// The comment line specifically, not the sections= line below it: that
	// one lists whatever is switched on, which is not the same question as
	// what could be.
	var advertised string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "# Which tabs to mirror.") {
			advertised = line
		}
	}
	if advertised == "" {
		t.Fatalf("the saved config no longer says which tabs exist:\n%s", body)
	}
	for _, sec := range sectionCatalogue() {
		if !strings.Contains(advertised, "'"+sec.ID+"'") {
			t.Errorf("the %q tab is not advertised in %q", sec.ID, advertised)
		}
	}
}

// ---------------------------------------------------------------------------
// A tab added in a later version
// ---------------------------------------------------------------------------

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func hasSection(cfg *Config, id string) bool {
	for _, s := range cfg.Sections {
		if s == id {
			return true
		}
	}
	return false
}

// Every existing install's config lists its tabs, so a tab added to the
// defaults reached nobody who already had one: Calendar, which brings quiz
// deadlines, would have been missing on every machine that updated.
func TestANewTabIsSwitchedOnInAnOlderConfig(t *testing.T) {
	cfg, err := LoadConfig(writeConfigFile(t,
		"sections = ['resources', 'overview', 'syllabus', 'announcements', 'assignments', 'dropbox']\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasSection(cfg, "calendar") {
		t.Errorf("an older config did not gain the new tab: %v", cfg.Sections)
	}
}

// A tab the file has been offered stays exactly as the student left it.
func TestATabTurnedOffStaysOff(t *testing.T) {
	path := writeConfigFile(t, "sections = ['resources', 'assignments']\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Tabs the older version offered and the student left out stay out.
	if hasSection(cfg, "overview") || hasSection(cfg, "dropbox") {
		t.Errorf("a tab the student had left out was switched back on: %v", cfg.Sections)
	}
	if !hasSection(cfg, "calendar") {
		t.Fatalf("the new tab was not offered: %v", cfg.Sections)
	}

	// Once saved, the new tab counts as offered: turning it off sticks.
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "known_sections = [") || !strings.Contains(string(body), "'calendar'") {
		t.Fatalf("the saved config does not record which tabs it has been offered:\n%s", body)
	}
	cfg.Sections = []string{"resources", "assignments"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if hasSection(again, "calendar") {
		t.Errorf("a tab turned off after it was offered came back: %v", again.Sections)
	}
}

// Loading a config never writes it: a dry run must leave the file alone, so
// the offer is only recorded by a save the student's own action caused.
func TestOfferingANewTabDoesNotWriteTheConfig(t *testing.T) {
	body := "sections = ['resources', 'assignments']\n"
	path := writeConfigFile(t, body)
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("loading the config rewrote it:\n%s", got)
	}
}
