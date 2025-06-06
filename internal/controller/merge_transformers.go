package controller

import (
	"reflect"
	"slices"

	"dario.cat/mergo"
	corev1 "k8s.io/api/core/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// convertToGenericSlice returns a slice where each item in the slice
// is converted to a T object from each item in reflect.Value
func convertToGenericSlice[T any](val reflect.Value) []T {
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Slice {
		return nil
	}

	var result []T
	for i := 0; i < val.Len(); i++ {
		item := val.Index(i).Interface()
		tItem, ok := item.(T)
		if !ok {
			return nil
		}
		result = append(result, tItem)
	}
	return result
}

// mergeKeyValue returns the value given the name of the field in that struct
// for example,
// myEnvVar := corev1.EnvVar{"Name": "env-var"}
// mergeKeyValue(myEnvVar, "Name") returns "env-var"
func mergeKeyValue[T any](obj T, fieldName string) string {
	return reflect.ValueOf(obj).FieldByName(fieldName).String()
}

// genericSliceTransformer merges two slices of the same type T
// mergeFunc is the function that contains logic for merging two T objects
// mergeKey is the name of the field in T, so that if dst.MergeKey == src.MergeKey,
// the mergeFunc is called on those two objects. Otherwise, the src is appended
// for now, only string fields are supported for mergeKey
// (since we cannot guarantee equality for generic reflect.Value)
// mergeFunc takes in
// - dst (pointer): so that in-place merge can take happen
// - src: the src object to merge into dst
func genericSliceTransformer[T any](
	typ reflect.Type,
	mergeFunc func(dst *T, src T) error,
	mergeKey string) func(dst, src reflect.Value) error {

	if typ == reflect.TypeOf([]T{}) {
		return func(dst, src reflect.Value) error {

			// Reject transforming anything other than slices
			if dst.Kind() != reflect.Slice || src.Kind() != reflect.Slice {
				return nil
			}

			srcSlice := convertToGenericSlice[T](src)
			dstSlice := convertToGenericSlice[T](dst)

			// keep track of the common mergeKeys among src and dst
			srcMergeKeyMap := map[string]T{}
			commonMergeKeys := []string{} // TODO: maybe mergeKey can be another generic type?

			for _, srcObj := range srcSlice {
				mergeKeyValue := mergeKeyValue(srcObj, mergeKey)
				srcMergeKeyMap[mergeKeyValue] = srcObj
			}

			for _, dstObj := range dstSlice {
				mergeKeyValue := mergeKeyValue(dstObj, mergeKey)
				if _, found := srcMergeKeyMap[mergeKeyValue]; found {
					commonMergeKeys = append(commonMergeKeys, mergeKeyValue)
				}
			}

			// now loop over dstSlice and see if there is a srcObj with same mergeKey value in src
			for i, dstObj := range dstSlice {

				dstMergeKeyValue := mergeKeyValue(dstObj, mergeKey)

				// Found a matching srcObj with same mergeKey value
				if srcObj, found := srcMergeKeyMap[dstMergeKeyValue]; found {

					// Calls mergeFunc on the logic that merges two T structs in the slice
					err := mergeFunc(&dstObj, srcObj)

					if err != nil {
						return err
					}

					// Update dstObj in dstSlice if merge was successful
					dstSlice[i] = dstObj
				}
			}

			// Construct the mergedSlice combining both src and dst
			mergedSlice := []T{}

			// mergedSlice contains everything already present in dst to begin with,
			// with the common T objects already merged from src
			mergedSlice = append(mergedSlice, dstSlice...)

			// append other src objects that weren't merged and skip the ones that are common
			for _, srcObj := range srcSlice {
				mergeKeyValue := mergeKeyValue(srcObj, mergeKey)
				if !slices.Contains(commonMergeKeys, mergeKeyValue) {
					mergedSlice = append(mergedSlice, srcObj)
				}
			}

			// Now rewrite dst with mergedSlice
			dst.Set(reflect.ValueOf(mergedSlice))
			return nil
		}
	}
	return nil
}

// envVarSliceTransformer: transformer for merging two EnvVars
type envVarSliceTransformer struct{}

// Transformer for []corev1.Env
func (e envVarSliceTransformer) Transformer(typ reflect.Type) func(dst, src reflect.Value) error {

	// mergeKey for merging two EnvVars is the Name of the EnvVar
	mergeKey := "Name"
	mergeFunc := func(dst *corev1.EnvVar, src corev1.EnvVar) error {
		return mergo.Merge(dst, src, mergo.WithOverride)
	}

	return genericSliceTransformer(typ, mergeFunc, mergeKey)
}

// stringSlicePrependTransformer: transformer for merging two string slices
type stringSlicePrependTransformer struct{}

// Transformer for []string, such as Container.Args so that src args get prepended, not appended
func (stringSlicePrependTransformer) Transformer(t reflect.Type) func(dst, src reflect.Value) error {
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String {
		return func(dst, src reflect.Value) error {
			// Ensure dst is settable
			if !dst.CanSet() {
				return nil
			}
			if src.IsNil() || src.Len() == 0 {
				return nil
			}

			// Combine: src first, then dst
			merged := reflect.AppendSlice(src, dst)
			dst.Set(merged)
			return nil
		}
	}
	return nil
}

// compositeTransformer is a list of transformers to apply in a single mergo.Merge call
type compositeTransformer struct {
	transformers []mergo.Transformers
}

// Transformer takes in a list of Transformers and applies them one by one
func (ct compositeTransformer) Transformer(t reflect.Type) func(dst, src reflect.Value) error {
	for _, tr := range ct.transformers {
		if fn := tr.Transformer(t); fn != nil {
			return fn
		}
	}
	return nil
}

// containerSliceTransformer: transformer for merging two Containers
type containerSliceTransformer struct{}

// Transformer merges two []corev1.Container based on their Name,
// and applies transformers for each Container.Spec fields
func (c containerSliceTransformer) Transformer(typ reflect.Type) func(dst, src reflect.Value) error {

	// mergeKey for merging two Containers is the Name of the Container
	mergeKey := "Name"

	// dstContainer (comes from baseconfig)
	// srcContainer (comes from msvc and controller logic)
	mergeFunc := func(dstContainer *corev1.Container, srcContainer corev1.Container) error {

		// Command should be completely overriden, not appended
		if len(srcContainer.Command) > 0 {
			dstContainer.Command = []string{}
		}

		err := mergo.Merge(dstContainer,
			srcContainer,
			mergo.WithAppendSlice,
			mergo.WithOverride,
			mergo.WithTransformers(compositeTransformer{
				transformers: []mergo.Transformers{
					envVarSliceTransformer{},
					stringSlicePrependTransformer{},
				},
			}),
		)

		if err != nil {
			return err
		}

		return nil
	}

	return genericSliceTransformer(typ, mergeFunc, mergeKey)
}

// parentRefSliceTransformer: transformer for merging two ParentReference objects
type parentRefSliceTransformer struct{}

// Transformer merges two []gatewayv1.ParentReference based on their Name,
// and applies transformers for each Container.Spec fields
func (c parentRefSliceTransformer) Transformer(typ reflect.Type) func(dst, src reflect.Value) error {

	// mergeKey for merging two ParentReference is the Name of the ParentReference
	mergeKey := "Name"

	// dstParentReference (comes from baseconfig)
	// srcParentReference (comes from msvc and controller logic)
	mergeFunc := func(dstParentReference *gatewayv1.ParentReference, srcParentReference gatewayv1.ParentReference) error {

		err := mergo.Merge(dstParentReference,
			srcParentReference,
			mergo.WithAppendSlice,
			mergo.WithOverride)

		if err != nil {
			return err
		}

		return nil
	}

	return genericSliceTransformer(typ, mergeFunc, mergeKey)
}

// backendRefTransformer: transformer for merging two BackendRef objects
type backendRefTransformer struct{}

// Transformer merges two []gatewayv1.BackendRef based on their Name,
// and applies transformers for each Container.Spec fields
func (c backendRefTransformer) Transformer(typ reflect.Type) func(dst, src reflect.Value) error {

	// mergeKey for merging two BackendRef is the Name of the BackendRef
	mergeKey := "Name"

	// dstBackendRef (comes from baseconfig)
	// srcBackendRef (comes from msvc and controller logic)
	mergeFunc := func(dstBackendRef *gatewayv1.BackendRef, srcBackendRef gatewayv1.BackendRef) error {

		err := mergo.Merge(dstBackendRef,
			srcBackendRef,
			mergo.WithAppendSlice,
			mergo.WithOverride)

		if err != nil {
			return err
		}

		return nil
	}

	return genericSliceTransformer(typ, mergeFunc, mergeKey)
}

// MergeContainerSlices merges src slice into dest in place
func MergeContainerSlices(dest, src []corev1.Container) ([]corev1.Container, error) {
	err := mergo.Merge(&dest, src, mergo.WithTransformers(containerSliceTransformer{}))

	if err != nil {
		return []corev1.Container{}, err
	}

	return dest, err
}

// MergeGatewayRefSlices merges src slice containing gatewayv1.ParentRefs into dest in place
func MergeGatewayRefSlices(dest, src []gatewayv1.ParentReference) ([]gatewayv1.ParentReference, error) {
	err := mergo.Merge(&dest, src, mergo.WithTransformers(parentRefSliceTransformer{}))

	if err != nil {
		return []gatewayv1.ParentReference{}, err
	}

	return dest, err
}

// MergeBackendRefSlices merges src slice containing gatewayv1.ParentRefs into dest in place
func MergeBackendRefSlices(dest, src []gatewayv1.BackendRef) ([]gatewayv1.BackendRef, error) {
	err := mergo.Merge(&dest, src, mergo.WithTransformers(backendRefTransformer{}))

	if err != nil {
		return []gatewayv1.BackendRef{}, err
	}

	return dest, err
}
