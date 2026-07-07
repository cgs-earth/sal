package pull

type PullCmd struct {
	Artifact    string `arg:"positional" help:"Full URL of the OCI artifact to pull. Example: ghcr.io/my-username/my-repository:latest"`
	Username    string `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry"`
	Password    string `arg:"--password,env:OCI_PASSWORD" help:"Password for the OCI registry"`
	Destination string `arg:"--destination" help:"Optional destination path to save the pulled artifact. If not specified, the artifact will be saved in the current working directory"`
}

func Run(cfg *PullCmd) error {
	return nil
}
