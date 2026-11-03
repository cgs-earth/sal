// Package terraform generates the Terraform configuration that deploys a
// built SAL data product on Google Cloud: a Cloud Storage bucket holding a
// copy of .sal/data, written by `sal upload`, and a Cloud Run service running
// `sal serve` over it, with the bucket mounted into the container so that
// DuckDB reads the Iceberg table as local files through Cloud Storage FUSE and
// the service account, not a key, is what grants access.
package terraform

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/cgs-earth/sal/pkg"
)

// DefaultDir is the directory under the project root the configuration is
// written to, and the one the generated Makefile points terraform at.
const DefaultDir = "terraform"

// DefaultImage is the container image the Cloud Run service runs, the one
// the sal repository publishes.
const DefaultImage = "ghcr.io/cgs-earth/sal:latest"

// DefaultRegion is the Google Cloud region the bucket and service are
// created in when none is given.
const DefaultRegion = "us-central1"

// MountPath is where the bucket is mounted inside the container, and so
// what `sal serve --data` is pointed at.
const MountPath = "/mnt/sal"

type TerraformCmd struct {
	Project string `arg:"--project" help:"the Google Cloud project ID to deploy into, recorded in terraform.tfvars; terraform asks for it otherwise"`
	Region  string `arg:"--region" default:"us-central1" help:"the region the bucket and the Cloud Run service are created in"`
	Image   string `arg:"--image" default:"ghcr.io/cgs-earth/sal:latest" help:"the sal container image the Cloud Run service runs"`
	Dir     string `arg:"--dir" default:"terraform" help:"the directory to write the configuration to, relative to the project root"`
	Force   bool   `arg:"--force" help:"replace files the directory already holds"`
}

// ErrExists is returned when the directory already holds a generated file
// and --force was not given, so that a configuration someone edited is not
// written over.
var ErrExists = errors.New("already exists; pass --force to replace it")

func (cmd *TerraformCmd) Run() error {
	root, err := pkg.SALProjectDir(os.UserHomeDir)
	if err != nil {
		return fmt.Errorf("sal terraform runs inside a SAL project: %w", err)
	}
	name, err := pkg.GitProjectName()
	if err != nil {
		return err
	}
	dir := cmd.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	written, err := Generate(dir, Options{Name: name, Project: cmd.Project, Region: cmd.Region, Image: cmd.Image, Force: cmd.Force})
	if err != nil {
		return err
	}
	for _, path := range written {
		slog.Info("wrote " + path)
	}
	slog.Info("Review " + filepath.Join(dir, "terraform.tfvars") + ", then `make apply` (terraform -chdir=" + cmd.Dir + " init && terraform -chdir=" + cmd.Dir + " apply) and `sal upload --bucket` the data product to the bucket it creates")
	return nil
}

// Options is what the configuration is generated from.
type Options struct {
	// Name is the project's name, the git repository name, which names the
	// service and the bucket and is the namespace the table sits under.
	Name string
	// Project is the Google Cloud project ID, or "" to leave it to be filled
	// in in terraform.tfvars.
	Project string
	Region  string
	Image   string
	// Force replaces files that already exist instead of refusing.
	Force bool
}

// slugPattern matches what a Cloud Run service name or a bucket name
// segment may not hold.
var slugPattern = regexp.MustCompile(`[^a-z0-9-]+`)

// Slug is the project name as a label Google Cloud accepts in a service, a
// service account, or a bucket name: lowercase letters, digits, and
// hyphens, starting with a letter, at most 30 characters so that a suffix
// still fits a service account ID.
func Slug(name string) string {
	slug := slugPattern.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" || slug[0] < 'a' || slug[0] > 'z' {
		slug = "sal-" + slug
	}
	if len(slug) > 30 {
		slug = strings.TrimRight(slug[:30], "-")
	}
	return slug
}

// Generate writes the configuration into dir and returns the paths written.
// A file that already exists is ErrExists unless Force is set, so that a
// configuration the user edited is never written over by a re-run.
func Generate(dir string, opts Options) ([]string, error) {
	if opts.Region == "" {
		opts.Region = DefaultRegion
	}
	if opts.Image == "" {
		opts.Image = DefaultImage
	}
	data := struct {
		Options
		Slug      string
		MountPath string
	}{Options: opts, Slug: Slug(opts.Name), MountPath: MountPath}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	var written []string
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if _, err := os.Stat(path); err == nil && !opts.Force {
			return written, fmt.Errorf("%s %w", path, ErrExists)
		} else if err != nil && !os.IsNotExist(err) {
			return written, err
		}
		var out strings.Builder
		if err := file.template.Execute(&out, data); err != nil {
			return written, err
		}
		if err := os.WriteFile(path, []byte(out.String()), 0644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// files are the generated files, each a template over Options.
var files = []struct {
	name     string
	template *template.Template
}{
	{"main.tf", tmpl("main.tf", mainTF)},
	{"variables.tf", tmpl("variables.tf", variablesTF)},
	{"outputs.tf", tmpl("outputs.tf", outputsTF)},
	{"terraform.tfvars", tmpl("terraform.tfvars", tfvars)},
	{".gitignore", tmpl(".gitignore", gitignore)},
	{"README.md", tmpl("README.md", readme)},
}

func tmpl(name string, text string) *template.Template {
	return template.Must(template.New(name).Parse(text))
}

const mainTF = `# Generated by sal terraform. A Cloud Run service running sal serve over a
# copy of this project's .sal/data in a Cloud Storage bucket. Edit freely; sal
# will not write over this file unless asked with --force.
#
# Workflow:
#   make apply                                 create the bucket and the service
#   sal upload --bucket gs://<bucket>/         copy .sal/data into the bucket (make upload)
#   open the service URL                       /sparql, /stac/catalog.json, and /blobs
#
# The bucket is mounted into the container through Cloud Storage FUSE, so sal
# reads the Iceberg table as local files and the service account's read access
# to the bucket is all the credential there is: no key is stored anywhere. The
# bucket and the service are in one region, so a query never leaves it.

terraform {
  required_version = ">= 1.5"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 6.0"
    }
  }
}

provider "google" {
  project = var.project
  region  = var.region
}

locals {
  # Bucket names are global, so the project ID is part of the default.
  bucket_name = coalesce(var.bucket, "${var.project}-${var.name}-sal")
  # Where the bucket appears inside the container; sal serve --data reads it.
  mount_path = "{{.MountPath}}"
}

resource "google_project_service" "run" {
  service            = "run.googleapis.com"
  disable_on_destroy = false
}

# The data product. sal upload writes the whole .sal/data layout here: the
# Iceberg table under <name>/triples, the pinned vocabulary documents under
# blobs/, and the STAC catalog under stac/.
resource "google_storage_bucket" "data" {
  name                        = local.bucket_name
  location                    = var.region
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = var.bucket_force_destroy

  versioning {
    enabled = false
  }
}

# The identity the service runs as, with read access to the bucket and
# nothing else.
resource "google_service_account" "serve" {
  account_id   = "${var.name}-sal-serve"
  display_name = "sal serve for ${var.name}"
}

resource "google_storage_bucket_iam_member" "serve_reads_data" {
  bucket = google_storage_bucket.data.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.serve.email}"
}

resource "google_cloud_run_v2_service" "serve" {
  name                = "${var.name}-sal"
  location            = var.region
  ingress             = "INGRESS_TRAFFIC_ALL"
  deletion_protection = false

  template {
    service_account = google_service_account.serve.email
    # Cloud Storage volumes need the second generation environment.
    execution_environment            = "EXECUTION_ENVIRONMENT_GEN2"
    timeout                          = "${var.request_timeout_seconds}s"
    max_instance_request_concurrency = var.concurrency

    scaling {
      min_instance_count = var.min_instances
      max_instance_count = var.max_instances
    }

    volumes {
      name = "sal-data"

      gcs {
        bucket        = google_storage_bucket.data.name
        read_only     = true
        mount_options = var.mount_options
      }
    }

    containers {
      image = var.image
      # sal serve over the mounted copy of .sal/data; --with-ui adds the
      # browser UI and the /api endpoints it needs, see variables.tf.
      args = concat(["serve", "--data", local.mount_path], var.with_ui ? ["--with-ui"] : [])

      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = var.cpu
          memory = var.memory
        }
        # CPU is only allocated while a request is being handled, which is
        # what makes an idle service free.
        cpu_idle          = true
        startup_cpu_boost = true
      }

      volume_mounts {
        name       = "sal-data"
        mount_path = local.mount_path
      }

      # sal opens DuckDB and reads the table's schema before it listens, so
      # the probe allows for that.
      startup_probe {
        initial_delay_seconds = 5
        period_seconds        = 10
        failure_threshold     = 12

        tcp_socket {
          port = 8080
        }
      }
    }
  }

  depends_on = [
    google_project_service.run,
    google_storage_bucket_iam_member.serve_reads_data,
  ]
}

# A SPARQL endpoint is usually meant to be queried by anyone; set public to
# false to require an identity token instead.
resource "google_cloud_run_v2_service_iam_member" "public" {
  count = var.public ? 1 : 0

  name     = google_cloud_run_v2_service.serve.name
  location = google_cloud_run_v2_service.serve.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}
`

const variablesTF = `# Generated by sal terraform. The defaults are deliberately small: one CPU,
# the memory DuckDB needs to open an Iceberg table with the spatial extension
# loaded, no instance kept warm, and at most two instances, so that an idle
# deployment costs nothing beyond the bucket. Raise them for a busier endpoint
# or a larger table.

variable "project" {
  description = "The Google Cloud project ID to deploy into."
  type        = string

  validation {
    condition     = length(var.project) > 0
    error_message = "Set project in terraform.tfvars (or TF_VAR_project) to the Google Cloud project ID."
  }
}

variable "region" {
  description = "The region the bucket and the Cloud Run service are created in. Keep them in one region so queries never cross regions."
  type        = string
  default     = "{{.Region}}"
}

variable "name" {
  description = "The name of the data product: the git repository name, which is the namespace the Iceberg table sits under and names the service and the bucket."
  type        = string
  default     = "{{.Slug}}"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,29}$", var.name))
    error_message = "name must be lowercase letters, digits, and hyphens, start with a letter, and be at most 30 characters."
  }
}

variable "image" {
  description = "The sal container image the service runs."
  type        = string
  default     = "{{.Image}}"
}

variable "bucket" {
  description = "The name of the bucket holding the data product. Defaults to <project>-<name>-sal, since bucket names are global."
  type        = string
  default     = null
}

variable "bucket_force_destroy" {
  description = "Whether terraform destroy deletes the bucket with the data product still in it. Off, so that a destroy never deletes data by accident; empty the bucket first or set this to true."
  type        = bool
  default     = false
}

variable "public" {
  description = "Whether anyone may call the service. Off requires a Google identity token on every request."
  type        = bool
  default     = true
}

variable "with_ui" {
  description = "Whether sal serve also serves its browser UI. The UI's /api/sql endpoint runs arbitrary DuckDB statements and /api/table points the queries at any bucket a caller names, so leave this off on a public service unless that is acceptable."
  type        = bool
  default     = false
}

variable "cpu" {
  description = "CPU limit of an instance."
  type        = string
  default     = "1"
}

variable "memory" {
  description = "Memory limit of an instance. DuckDB holds the Iceberg manifests and the spatial extension in memory; raise this for a large table."
  type        = string
  default     = "2Gi"
}

variable "min_instances" {
  description = "Instances kept warm. Zero means the first request after idle pays the cold start; one keeps the endpoint responsive at a steady cost."
  type        = number
  default     = 0
}

variable "max_instances" {
  description = "The most instances the service scales to."
  type        = number
  default     = 2
}

variable "concurrency" {
  description = "Requests an instance handles at once. Every query runs on one DuckDB handle, so this bounds how many share it."
  type        = number
  default     = 20
}

variable "request_timeout_seconds" {
  description = "How long a request, and so a query, may take."
  type        = number
  default     = 300
}

variable "mount_options" {
  description = "Cloud Storage FUSE options for the mounted bucket. Iceberg metadata and data files never change in place, so caching their metadata saves a round trip to the bucket per file on every query; sal upload writes new file names, which a new instance sees at once and a running one within this many seconds."
  type        = list(string)
  default     = ["metadata-cache-ttl-secs=300"]
}
`

const outputsTF = `# Generated by sal terraform.

output "service_url" {
  description = "Where the service answers; /sparql is the SPARQL endpoint and /stac/catalog.json the STAC catalog."
  value       = google_cloud_run_v2_service.serve.uri
}

output "sparql_endpoint" {
  value = "${google_cloud_run_v2_service.serve.uri}/sparql"
}

output "bucket" {
  description = "The bucket holding the data product."
  value       = google_storage_bucket.data.name
}

output "bucket_url" {
  description = "What to pass to sal upload --bucket: the bucket root, so that the whole .sal/data layout is uploaded."
  value       = "gs://${google_storage_bucket.data.name}/"
}

output "service_account" {
  value = google_service_account.serve.email
}
`

const tfvars = `# Generated by sal terraform. Values for the variables in variables.tf; every
# other variable has a default there.
{{if .Project}}project = "{{.Project}}"{{else}}# The Google Cloud project ID to deploy into (required).
project = ""{{end}}
# region = "{{.Region}}"
# public = true
# with_ui = false
`

const gitignore = `# Terraform's working files and state. State holds resource IDs and the
# service URL, nothing secret, but it belongs in a remote backend rather than
# in git once more than one person applies this.
.terraform/
*.tfstate
*.tfstate.*
crash.log
crash.*.log
`

const readme = `# Deploying {{.Name}} on Google Cloud

Generated by ` + "`sal terraform`" + `. This configuration creates:

- a Cloud Storage bucket holding a copy of ` + "`.sal/data`" + `, the built data product;
- a Cloud Run service running ` + "`sal serve`" + ` over that copy, which answers SPARQL at ` + "`/sparql`" + `,
  serves the STAC catalog at ` + "`/stac/catalog.json`" + `, and the pinned vocabulary documents at ` + "`/blobs`" + `;
- a service account the service runs as, with read access to the bucket and nothing else.

The bucket is mounted into the container with Cloud Storage FUSE at ` + "`{{.MountPath}}`" + `, so DuckDB reads
the Iceberg table as local files and no key is stored anywhere. Both live in one region.

## Use

` + "```sh" + `
# once: set project in terraform.tfvars, then
make apply          # terraform init && terraform apply, from the project root
make upload         # sal upload --bucket gs://<bucket>/, after every sal build
make destroy        # terraform destroy; the bucket must be empty unless bucket_force_destroy is true
` + "```" + `

` + "`terraform output service_url`" + ` prints the service URL. The service reads what the bucket holds when an
instance starts and caches file metadata for ` + "`mount_options`" + ` seconds, so a fresh upload is visible to a
new instance at once and to a running one within that time.

The defaults in ` + "`variables.tf`" + ` are small on purpose: one CPU, 2 GiB, no instance kept warm, at most two.
` + "`with_ui`" + ` is off because the UI's SQL endpoint runs arbitrary DuckDB statements; turn it on behind
` + "`public = false`" + ` or on a service you trust the callers of.
`
