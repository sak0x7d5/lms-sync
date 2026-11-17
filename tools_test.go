package main

import (
	"errors"
	"reflect"
	"testing"
)

func onPath(programs ...string) func(string) bool {
	return func(p string) bool {
		for _, q := range programs {
			if p == q {
				return true
			}
		}
		return false
	}
}

// Every machine the installers reach gets the command its own package
// manager takes, with sudo only where it is needed and available.
func TestToolInstallPlanFitsTheMachine(t *testing.T) {
	both := []string{"poppler", "tesseract"}
	cases := []struct {
		name string
		env  toolEnv
		want [][]string
	}{
		{"ubuntu as a user", toolEnv{GOOS: "linux", Has: onPath("apt-get", "sudo")}, [][]string{
			{"sudo", "apt-get", "update"},
			{"sudo", "apt-get", "install", "-y", "poppler-utils", "tesseract-ocr"},
		}},
		{"ubuntu as root", toolEnv{GOOS: "linux", Root: true, Has: onPath("apt-get")}, [][]string{
			{"apt-get", "update"},
			{"apt-get", "install", "-y", "poppler-utils", "tesseract-ocr"},
		}},
		{"arch brings the english data", toolEnv{GOOS: "linux", Has: onPath("pacman", "sudo")}, [][]string{
			{"sudo", "pacman", "-S", "--needed", "--noconfirm", "poppler", "tesseract", "tesseract-data-eng"},
		}},
		{"mac with homebrew never uses sudo", toolEnv{GOOS: "darwin", Has: onPath("brew", "sudo")}, [][]string{
			{"brew", "install", "poppler", "tesseract"},
		}},
		{"termux uses pkg, not the apt it also has", toolEnv{GOOS: "linux", Termux: true, Has: onPath("pkg", "apt-get")}, [][]string{
			{"pkg", "install", "-y", "poppler", "tesseract"},
		}},
		{"windows installs one id at a time", toolEnv{GOOS: "windows", Has: onPath("winget")}, [][]string{
			{"winget", "install", "--exact", "--silent", "--accept-source-agreements", "--accept-package-agreements", "--id", "oschwartz10612.Poppler"},
			{"winget", "install", "--exact", "--silent", "--accept-source-agreements", "--accept-package-agreements", "--id", "UB-Mannheim.TesseractOCR"},
		}},
	}
	for _, c := range cases {
		plan, err := planToolInstall(c.env, both)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(plan.Commands, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, plan.Commands, c.want)
		}
	}
}

func TestToolInstallPlanAsksOnlyForWhatIsMissing(t *testing.T) {
	plan, err := planToolInstall(toolEnv{GOOS: "darwin", Has: onPath("brew")}, []string{"tesseract"})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"brew", "install", "tesseract"}}; !reflect.DeepEqual(plan.Commands, want) {
		t.Errorf("got %q, want %q", plan.Commands, want)
	}
	plan, err = planToolInstall(toolEnv{GOOS: "linux", Has: onPath("apt-get")}, nil)
	if err != nil || len(plan.Commands) != 0 {
		t.Errorf("nothing missing should run nothing, got %q, %v", plan.Commands, err)
	}
}

// A package manager on PATH is only used on the kind of machine it belongs
// to: a desktop with a `pkg` is not a phone, and a Mac's apt-get (from some
// toolchain) is not how a Mac installs things.
func TestToolInstallPlanIgnoresManagersFromOtherMachines(t *testing.T) {
	if _, err := planToolInstall(toolEnv{GOOS: "linux", Has: onPath("pkg")}, []string{"poppler"}); !errors.Is(err, errNoManager) {
		t.Errorf("pkg off Termux: got %v, want errNoManager", err)
	}
	if _, err := planToolInstall(toolEnv{GOOS: "darwin", Has: onPath("apt-get", "sudo")}, []string{"poppler"}); !errors.Is(err, errNoManager) {
		t.Errorf("apt-get on a Mac: got %v, want errNoManager", err)
	}
	if _, err := planToolInstall(toolEnv{GOOS: "windows", Has: onPath()}, []string{"poppler"}); !errors.Is(err, errNoManager) {
		t.Errorf("Windows without winget: got %v, want errNoManager", err)
	}
}

func TestToolInstallPlanNeedsAWayToBecomeRoot(t *testing.T) {
	plan, err := planToolInstall(toolEnv{GOOS: "linux", Has: onPath("dnf")}, []string{"poppler"})
	if !errors.Is(err, errNeedsRoot) || plan.Manager != "dnf" {
		t.Errorf("got %+v, %v; want dnf and errNeedsRoot", plan, err)
	}
}

// Every helper has a package on every manager, or a plan would silently
// install half of what it said.
func TestEveryManagerKnowsEveryHelper(t *testing.T) {
	for _, m := range pkgManagers {
		for _, h := range helperTools {
			if m.Packages[h.ID] == "" {
				t.Errorf("%s has no package for %s", m.ID, h.ID)
			}
		}
	}
}

func TestEnterMeansYes(t *testing.T) {
	for in, want := range map[string]bool{"\n": true, "y\n": true, "YES\n": true, "n\n": false, "no\n": false, "later\n": false} {
		if got := answerIsYes(in); got != want {
			t.Errorf("answerIsYes(%q) = %v, want %v", in, got, want)
		}
	}
}
