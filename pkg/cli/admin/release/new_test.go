package release

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	imageapi "github.com/openshift/api/image/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func writeImageReferences(t *testing.T, dir string, tagNames []string) {
	t.Helper()
	is := &imageapi.ImageStream{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageStream",
			APIVersion: "image.openshift.io/v1",
		},
	}
	for _, name := range tagNames {
		is.Spec.Tags = append(is.Spec.Tags, imageapi.TagReference{
			Name: name,
			From: &corev1.ObjectReference{
				Kind: "DockerImage",
				Name: "example.com/" + name + ":latest",
			},
		})
	}
	data, err := json.Marshal(is)
	if err != nil {
		t.Fatalf("failed to marshal image-references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image-references"), data, 0644); err != nil {
		t.Fatalf("failed to write image-references: %v", err)
	}
}

func tagNames(is *imageapi.ImageStream) []string {
	var names []string
	for _, tag := range is.Spec.Tags {
		names = append(names, tag.Name)
	}
	return names
}

func TestPruneUnreferencedImageStreams(t *testing.T) {
	makeIS := func(names ...string) *imageapi.ImageStream {
		is := &imageapi.ImageStream{}
		for _, name := range names {
			is.Spec.Tags = append(is.Spec.Tags, imageapi.TagReference{
				Name: name,
				From: &corev1.ObjectReference{Kind: "DockerImage", Name: "example.com/" + name + ":latest"},
			})
		}
		return is
	}

	t.Run("images referenced by operator image-references are kept", func(t *testing.T) {
		dir := t.TempDir()
		operatorDir := filepath.Join(dir, "my-operator")
		if err := os.MkdirAll(operatorDir, 0777); err != nil {
			t.Fatal(err)
		}
		writeImageReferences(t, operatorDir, []string{"helper-image"})

		is := makeIS("my-operator", "helper-image", "unreferenced-image")
		metadata := map[string]imageData{
			"my-operator": {Directory: operatorDir},
		}

		if err := pruneUnreferencedImageStreams(&bytes.Buffer{}, is, metadata, []string{"my-operator"}); err != nil {
			t.Fatal(err)
		}

		names := tagNames(is)
		if !slices.Contains(names, "my-operator") {
			t.Error("expected my-operator to be kept (in include list)")
		}
		if !slices.Contains(names, "helper-image") {
			t.Error("expected helper-image to be kept (referenced by operator image-references)")
		}
		if slices.Contains(names, "unreferenced-image") {
			t.Error("expected unreferenced-image to be pruned")
		}
	})

	t.Run("base image image-references prevents pruning", func(t *testing.T) {
		dir := t.TempDir()

		operatorDir := filepath.Join(dir, "my-operator")
		if err := os.MkdirAll(operatorDir, 0777); err != nil {
			t.Fatal(err)
		}
		writeImageReferences(t, operatorDir, []string{"operator-dep"})

		baseDir := filepath.Join(dir, "cluster-version-operator")
		if err := os.MkdirAll(baseDir, 0777); err != nil {
			t.Fatal(err)
		}
		writeImageReferences(t, baseDir, []string{"cluster-update-console-plugin"})

		is := makeIS("cluster-version-operator", "my-operator", "operator-dep", "cluster-update-console-plugin")
		metadata := map[string]imageData{
			"my-operator":              {Directory: operatorDir},
			"cluster-version-operator": {Directory: baseDir},
		}

		if err := pruneUnreferencedImageStreams(&bytes.Buffer{}, is, metadata, []string{"cluster-version-operator", "my-operator"}); err != nil {
			t.Fatal(err)
		}

		names := tagNames(is)
		if !slices.Contains(names, "cluster-update-console-plugin") {
			t.Error("expected cluster-update-console-plugin to be kept (referenced by base image image-references)")
		}
		if !slices.Contains(names, "operator-dep") {
			t.Error("expected operator-dep to be kept (referenced by operator image-references)")
		}
	})

	t.Run("without base image image-references the image is pruned", func(t *testing.T) {
		dir := t.TempDir()

		operatorDir := filepath.Join(dir, "my-operator")
		if err := os.MkdirAll(operatorDir, 0777); err != nil {
			t.Fatal(err)
		}
		writeImageReferences(t, operatorDir, []string{"operator-dep"})

		is := makeIS("cluster-version-operator", "my-operator", "operator-dep", "cluster-update-console-plugin")
		metadata := map[string]imageData{
			"my-operator": {Directory: operatorDir},
		}

		if err := pruneUnreferencedImageStreams(&bytes.Buffer{}, is, metadata, []string{"cluster-version-operator", "my-operator"}); err != nil {
			t.Fatal(err)
		}

		names := tagNames(is)
		if slices.Contains(names, "cluster-update-console-plugin") {
			t.Error("expected cluster-update-console-plugin to be pruned (not referenced by any image-references)")
		}
	})
}

func TestBaseImageExcludedFromOrdered(t *testing.T) {
	baseTag := "cluster-version-operator"

	t.Run("base image tag is removed from ordered", func(t *testing.T) {
		ordered := []string{"my-operator", "cluster-version-operator", "another-operator"}
		ordered = slices.DeleteFunc(ordered, func(s string) bool {
			return s == baseTag
		})
		if slices.Contains(ordered, baseTag) {
			t.Errorf("expected %s to be removed from ordered", baseTag)
		}
		if len(ordered) != 2 {
			t.Errorf("expected 2 entries, got %d", len(ordered))
		}
	})

	t.Run("ordered unchanged when base image not present", func(t *testing.T) {
		ordered := []string{"my-operator", "another-operator"}
		ordered = slices.DeleteFunc(ordered, func(s string) bool {
			return s == baseTag
		})
		if len(ordered) != 2 {
			t.Errorf("expected 2 entries, got %d", len(ordered))
		}
	})
}

func TestMirrorImages(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		is                  *imageapi.ImageStream
		expectedWarningMsgs []string
		expectedErr         string
	}{
		{
			is:                  nil,
			expectedWarningMsgs: []string{},
			expectedErr:         "unable to retrieve release image info: must specify an image containing a release payload with --from",
		},
		{
			is: &imageapi.ImageStream{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{},
				Spec:       imageapi.ImageStreamSpec{},
				Status:     imageapi.ImageStreamStatus{},
			},
			expectedWarningMsgs: []string{
				"warning: No release authenticity verification is configured, all releases are considered unverified",
				"warning: An image was retrieved that failed verification: verification is not possible",
				"warning: Release image contains no image references - is this a valid release?",
			},
			expectedErr: "",
		},
		{
			is: &imageapi.ImageStream{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{},
				Spec: imageapi.ImageStreamSpec{
					LookupPolicy: imageapi.ImageLookupPolicy{},
					Tags: []imageapi.TagReference{
						{
							Name: "test",
							From: &corev1.ObjectReference{
								Name: "quay.io/test/other@sha256:0000000000000000000000000000000000000001",
								Kind: "DockerImage",
							},
						},
					},
				},
				Status: imageapi.ImageStreamStatus{},
			},
			expectedWarningMsgs: []string{
				"No release authenticity verification is configured, all releases are considered unverified",
				"warning: An image was retrieved that failed verification: verification is not possible",
				"warning: Release image contains no image references - is this a valid release?",
			},
			expectedErr: "release tag \"test\" is not valid: invalid checksum digest length",
		},
	}

	ioStream, _, _, errOut := genericiooptions.NewTestIOStreams()

	for _, tt := range tests {
		options := NewNewOptions(ioStream)
		err := options.mirrorImages(ctx, tt.is)

		if err != nil {
			if len(tt.expectedErr) == 0 {
				t.Fatalf("unexpected error occurred %v\n", err)
			}

			if err.Error() != tt.expectedErr {
				t.Fatalf("expected error %v but actual %v\n", tt.expectedErr, err.Error())
			}
		} else {
			if len(tt.expectedErr) > 0 {
				t.Fatalf("expected error %v but got none\n", tt.expectedErr)
			}
		}

		if len(tt.expectedWarningMsgs) == 0 && len(errOut.String()) > 0 {
			t.Fatalf("unexpected error %v fired\n", errOut.String())
		}

		for _, expectedErr := range tt.expectedWarningMsgs {
			if !strings.Contains(errOut.String(), expectedErr) {
				t.Fatalf("error %v expected but not fired\n", expectedErr)
			}
		}
	}
}
