// Command qs-admin is the operator CLI. `qs-admin bootstrap` initialises an
// installation (spec 13 §1.1, 15 §3.3): region, zones, address pools,
// instance types, public images, a test organization and user, and prints
// a `qs` profile. The services it calls do not exist yet, so this skeleton
// validates the bootstrap file and prints the plan (T-1.68 executes it).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"

	"github.com/mindevis/qstack/pkg/version"
)

const binary = "qs-admin"

// Bootstrap is the bootstrap file (deploy/l0/bootstrap.yaml).
type Bootstrap struct {
	Region struct {
		ID      string `yaml:"id"`      // e.g. ru-central1
		Country string `yaml:"country"` // ISO 3166-1 alpha-2; sets the billing currency
	} `yaml:"region"`
	Zones         []string `yaml:"zones"`
	AddressPools  []Pool   `yaml:"address_pools"`
	InstanceTypes []string `yaml:"instance_types"`
	Images        []string `yaml:"images"`
	Organization  struct {
		Name       string `yaml:"name"`
		AdminEmail string `yaml:"admin_email"`
	} `yaml:"organization"`
}

// Pool is a public address pool.
type Pool struct {
	Name string `yaml:"name"`
	CIDR string `yaml:"cidr"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Validate checks the file.
func (b *Bootstrap) Validate() error {
	var errs []error
	if !nameRe.MatchString(b.Region.ID) {
		errs = append(errs, fmt.Errorf("region.id %q is not a name", b.Region.ID))
	}
	if !countryRe.MatchString(b.Region.Country) {
		errs = append(errs, fmt.Errorf("region.country %q must be ISO 3166-1 alpha-2", b.Region.Country))
	}
	if len(b.Zones) == 0 {
		errs = append(errs, errors.New("at least one zone is required"))
	}
	for _, z := range b.Zones {
		if !nameRe.MatchString(z) {
			errs = append(errs, fmt.Errorf("zone %q is not a name", z))
		}
	}
	if b.Organization.Name == "" || b.Organization.AdminEmail == "" {
		errs = append(errs, errors.New("organization.name and organization.admin_email are required"))
	}
	return errors.Join(errs...)
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println(version.String(binary))
	case "bootstrap":
		if err := bootstrap(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, binary+" bootstrap:", err)
			os.Exit(1)
		}
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "usage: %s bootstrap -file <bootstrap.yaml> [-dry-run]\n       %s version\n", binary, binary)
}

func bootstrap(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	file := fs.String("file", "", "bootstrap file")
	dryRun := fs.Bool("dry-run", false, "validate and print the plan only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("-file is required")
	}
	b, err := load(*file)
	if err != nil {
		return err
	}
	plan := fmt.Sprintf("region %s (%s), zones %v\naddress pools %d, instance types %d, images %d\norganization %q, admin %s\n",
		b.Region.ID, b.Region.Country, b.Zones, len(b.AddressPools), len(b.InstanceTypes), len(b.Images),
		b.Organization.Name, b.Organization.AdminEmail)
	if *dryRun {
		_, err := io.WriteString(out, plan+"dry run: nothing created\n")
		return err
	}
	if _, err := io.WriteString(out, plan); err != nil {
		return err
	}
	return errors.New("creating resources needs the control plane services (roadmap T-1.68); use -dry-run")
}

func load(path string) (*Bootstrap, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var b Bootstrap
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := b.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &b, nil
}
