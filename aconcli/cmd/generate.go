// Copyright © 2023 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/client"
	"github.com/spf13/cobra"

	"aconcli/fileutil"
	"aconcli/repo"
)

var generateCmd = &cobra.Command{
	Use:   "generate DOCKER_IMAGE",
	Short: "Generate a manifest and commit file system layers to ACON repository",
	Long: `
Generate a manifest file in JSON format and commit to ACON repository the file
system layers from specified Docker image. The resulting manifest file can be
further edited manually and then be signed using the 'sign' sub-command.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return generateManifest(args)
	},
}

func generateManifest(args []string) error {
	dockerImageId = args[0]
	startingDir := filepath.Dir(manifestFile)
	if targetDir != "" {
		startingDir = targetDir
	}
	r, err := repo.FindRepo(startingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Generate Manifest: %v\n", err)
		return err
	}

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv,
		client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Generate Manifest: %v\n", err)
		return err
	}

	// get and process the image layers
	imageStream, err := cli.ImageSave(ctx, []string{dockerImageId})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Generate Manifest: cannot get image content: %v\n", err)
		return err
	}
	defer imageStream.Close()

	// get and transform the layer info
	inspect, _, err := cli.ImageInspectWithRaw(ctx, dockerImageId)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Generate Manifest: cannot inspect image: %v\n", err)
		return err
	}

	diffIds := []string{}
	dup := make([]bool, len(inspect.RootFS.Layers))
	duplicate := make(map[string]int)
	for i, layer := range inspect.RootFS.Layers {
		if _, found := duplicate[layer]; !found {
			diffIds = append(diffIds, layer)
			dup[i] = false
			duplicate[layer]++
		} else {
			dup[i] = true
		}
	}

	names, layers, err := fileutil.UntarBlob(imageStream)
	if err != nil {
		return err
	}

	orderedLayers := [][]byte{}
	for i, name := range names {
		if !dup[i] {
			orderedLayers = append(orderedLayers, layers[name])
		}
	}
	if err := r.CommitBlob(orderedLayers, diffIds); err != nil {
		return err
	}

	primaryLayers := make([]string, len(diffIds))
	for i, layer := range diffIds {
		converted, err := r.PrimaryDigest(layer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Generate Manifest: cannot get primary hash for layer %s: %v\n", layer, err)
			return err
		}
		primaryLayers[i] = converted
	}
	// write to specified manifest file
	w := repo.Workload{MaxInstance: 1,
		WorkingDir: inspect.Config.WorkingDir,
		Entrypoint: inspect.Config.Entrypoint,
		Env:        inspect.Config.Env,
		Signals:    make([]int32, 0),
		Uids:       make([]uint32, 0),
		LogFDs:     make([]uint32, 0)}

	_, err = os.Stat(manifestFile)
	if err == nil {
		// manifest file exists, update layers only
		content, err := os.ReadFile(manifestFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Generate Manifest: cannot read manifest %s: %v\n", manifestFile, err)
			return err
		}
		if err := json.Unmarshal(content, &w); err != nil {
			fmt.Fprintf(os.Stderr, "Generate Manifest: cannot unmarshal manifest %s: %v\n", manifestFile, err)
			return err
		}
	}
	w.Layer = primaryLayers
	m, _ := json.MarshalIndent(w, "", "    ")
	if err := os.WriteFile(manifestFile, m, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Generate Manifest: cannot write to %s: %v\n", manifestFile, err)
		return err
	}
	return nil
}

func init() {
	rootCmd.AddCommand(generateCmd)
	generateCmd.Flags().StringVarP(&manifestFile, "output", "o", "",
		"output file name for the manifest")
	generateCmd.MarkFlagRequired("output")
}
