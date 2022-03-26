package cli

import (
	"context"

	"github.com/alecthomas/kingpin"

	"github.com/kopia/kopia/snapshot/policy"
)

type policyUploadFlags struct {
	maxParallelUploads   string
	maxParallelFileReads string
}

func (c *policyUploadFlags) setup(cmd *kingpin.CmdClause) {
	cmd.Flag("max-parallel-file-reads", "Maximum number of parallel file reads").StringVar(&c.maxParallelFileReads)
	cmd.Flag("max-parallel-snapshots", "Maximum number of parallel snapshots (server, KopiaUI only)").StringVar(&c.maxParallelUploads)
}

func (c *policyUploadFlags) setUploadPolicyFromFlags(ctx context.Context, up *policy.UploadPolicy, changeCount *int) error {
	if err := applyOptionalInt(ctx, "max parallel file reads", &up.MaxParallelFileReads, c.maxParallelFileReads, changeCount); err != nil {
		return err
	}

	if err := applyOptionalInt(ctx, "max parallel snapshots", &up.MaxParallelSnapshots, c.maxParallelUploads, changeCount); err != nil {
		return err
	}

	return nil
}
