package controller

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"

	msv1alpha1 "github.com/llm-d/llm-d-model-service/api/v1alpha1"
)

const PVC_NAME = "my-pvc"
const MODEL_PATH = "path/to/model"
const HF_REPO_ID = "ibm-granite"
const HF_MODEL_ID = "granite-3.3-2b-instruct"

var _ = Describe("Model Artifacts", func() {
	Context("Given a model artifact with an invalid URI prefix", func() {
		modelArtifact := msv1alpha1.ModelArtifacts{
			URI: fmt.Sprintf("nothing://%s/%s", PVC_NAME, MODEL_PATH),
		}

		It("should parse correctly", func() {
			By("checking type of uri")
			Expect(isPVCURI(modelArtifact.URI)).To(BeFalse())
			Expect(isHFURI(modelArtifact.URI)).To(BeFalse())

			By("Parsing PVC uri should fail")
			_, err := parsePVCURI(&modelArtifact)
			Expect(err).NotTo(BeNil())

			By("Parsing HF uri should fail")
			_, _, err = parseHFURI(&modelArtifact)
			Expect(err).NotTo(BeNil())
		})
	})

	Context("Given an URI string", func() {
		tests := map[string]struct {
			expectedURIType        URIType
			expectedModelMountPath string
		}{
			"pvc://pvc-name/path/to/model": {
				expectedURIType:        PVC,
				expectedModelMountPath: modelStorageRoot + pathSep + "path/to/model",
			},
			"oci://repo-with-tag::path/to/model": {
				expectedURIType:        OCI,
				expectedModelMountPath: "", // TODO
			},
			"hf://repo-id/model-id": {
				expectedURIType:        HF,
				expectedModelMountPath: modelStorageRoot,
			},
			"pvc://pvc-name": {
				expectedURIType:        PVC,
				expectedModelMountPath: "",
			},
			"oci://": {
				expectedURIType:        OCI,
				expectedModelMountPath: "", // TODO
			},
			"hf://wrong": {
				expectedURIType:        HF,
				expectedModelMountPath: modelStorageRoot,
			},
			"random://": {
				expectedURIType:        UnknownURI,
				expectedModelMountPath: "",
			},
			"": {
				expectedURIType:        UnknownURI,
				expectedModelMountPath: "",
			},
			"PVC://": {
				expectedURIType:        UnknownURI,
				expectedModelMountPath: "",
			},
			"HF://": {
				expectedURIType:        UnknownURI,
				expectedModelMountPath: "",
			},
			"OCI://": {
				expectedURIType:        UnknownURI,
				expectedModelMountPath: "",
			},
		}

		It("should determine the type of the URI correctly", func() {
			for uri, answer := range tests {
				expectedURIType := answer.expectedURIType
				actualURIType := UriType(uri)
				Expect(actualURIType).To(Equal(expectedURIType))
			}
		})

		It("should compute the mounted model path correctly", func() {
			for uri, answer := range tests {
				expectedModelMountPath := answer.expectedModelMountPath

				actualModelMountPath, err := mountedModelPath(&msv1alpha1.ModelService{
					Spec: msv1alpha1.ModelServiceSpec{
						ModelArtifacts: msv1alpha1.ModelArtifacts{
							URI: uri,
						},
					},
				})

				// Expect error if uri type is unknown
				if answer.expectedURIType == UnknownURI {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).ToNot(HaveOccurred())
					Expect(actualModelMountPath).To(Equal(expectedModelMountPath))
				}

			}
		})
	})

	Context("Given a model artifact with a valid PVC URI", func() {
		ctx := context.Background()
		modelArtifact := msv1alpha1.ModelArtifacts{
			URI: fmt.Sprintf("pvc://%s/%s", PVC_NAME, MODEL_PATH),
		}

		modelService := msv1alpha1.ModelService{
			Spec: msv1alpha1.ModelServiceSpec{
				ModelArtifacts: modelArtifact,
			},
		}

		It("should parse correctly", func() {
			By("checking type of uri")
			Expect(isPVCURI(modelArtifact.URI)).To(BeTrue())
			Expect(isHFURI(modelArtifact.URI)).To(BeFalse())
			Expect(isOCIURI(modelArtifact.URI)).To(BeFalse())

			By("Parsing uri parts should be successful")
			parts, err := parsePVCURI(&modelArtifact)
			Expect(err).To(BeNil())
			Expect(len(parts) > 1).To(BeTrue())
			Expect(parts[0]).To(Equal(PVC_NAME))
			Expect(strings.Join(parts[1:], "/")).To(Equal(MODEL_PATH))
		})
		It("should produce a valid volumeMounts list", func() {
			volumeMounts := getVolumeMountsForContainer(ctx, &modelService)
			Expect(len(volumeMounts)).To(Equal(1))
			firstVolumeMount := volumeMounts[0]

			Expect(firstVolumeMount.Name).To(Equal(modelStorageVolumeName))
			Expect(firstVolumeMount.MountPath).To(Equal(modelStorageRoot))
			Expect(firstVolumeMount.ReadOnly).To(BeTrue())
		})
		It("should produce a valid volumes list", func() {
			volumes := getVolumeForPDDeployment(ctx, &modelService)
			Expect(len(volumes)).To(Equal(1))
			firstVolume := volumes[0]
			Expect(firstVolume.Name).To(Equal(modelStorageVolumeName))
			Expect(firstVolume.PersistentVolumeClaim.ClaimName).To(Equal(PVC_NAME))
			Expect(firstVolume.PersistentVolumeClaim.ReadOnly).To(BeTrue())
		})

		It("should produce a valid env list", func() {
			envs := getEnvsForContainer(ctx, &modelService)
			Expect(len(envs)).To(Equal(0))
		})
	})

	Context("Given a model artifact with a valid HF URI", func() {

		ctx := context.Background()
		authSecretName := "auth-secret-key"
		sizeLimit := "5Gi"
		sizeLimitQuan := resource.MustParse(sizeLimit)

		modelArtifact := msv1alpha1.ModelArtifacts{
			URI:            fmt.Sprintf("hf://%s/%s", HF_REPO_ID, HF_MODEL_ID),
			AuthSecretName: &authSecretName,
			Size:           &sizeLimitQuan,
		}

		modelService := msv1alpha1.ModelService{
			Spec: msv1alpha1.ModelServiceSpec{
				ModelArtifacts: modelArtifact,
			},
		}

		It("should parse correctly", func() {
			By("checking type of uri")
			Expect(isPVCURI(modelArtifact.URI)).To(BeFalse())
			Expect(isHFURI(modelArtifact.URI)).To(BeTrue())
			Expect(isOCIURI(modelArtifact.URI)).To(BeFalse())

			By("Parsing uri parts should be successful")
			repo, model, err := parseHFURI(&modelArtifact)
			Expect(err).To(BeNil())
			Expect(repo).To(Equal(HF_REPO_ID))
			Expect(model).To(Equal(HF_MODEL_ID))
		})

		It("should produce a valid volumeMounts list", func() {
			volumeMounts := getVolumeMountsForContainer(ctx, &modelService)
			Expect(len(volumeMounts)).To(Equal(1))
			firstVolumeMount := volumeMounts[0]

			Expect(firstVolumeMount.Name).To(Equal(modelStorageVolumeName))
			Expect(firstVolumeMount.MountPath).To(Equal(modelStorageRoot))
			Expect(firstVolumeMount.ReadOnly).To(BeFalse())
		})

		It("should produce a valid volumes list", func() {
			volumes := getVolumeForPDDeployment(ctx, &modelService)
			Expect(len(volumes)).To(Equal(1))
			firstVolume := volumes[0]
			Expect(firstVolume.Name).To(Equal(modelStorageVolumeName))
			Expect(firstVolume.EmptyDir.SizeLimit.String()).To(Equal(sizeLimit))
		})

		It("should produce a valid env list", func() {
			envs := getEnvsForContainer(ctx, &modelService)
			Expect(len(envs)).To(Equal(2))
			hfTokenEnvVar := envs[0]

			Expect(hfTokenEnvVar.Name).To(Equal(ENV_HF_TOKEN))
			Expect(hfTokenEnvVar.ValueFrom.SecretKeyRef.Name).To(Equal(authSecretName))
			Expect(hfTokenEnvVar.ValueFrom.SecretKeyRef.Key).To(Equal(ENV_HF_TOKEN))

			hfHomeEnvVar := envs[1]
			Expect(hfHomeEnvVar.Name).To(Equal(ENV_HF_HOME))
			Expect(hfHomeEnvVar.Value).To(Equal(modelStorageRoot))
		})
	})
})
