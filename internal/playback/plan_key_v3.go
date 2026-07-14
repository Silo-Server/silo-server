package playback

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

func DeterministicPlanIDV3(attemptID string, requestedFileID, effectiveFileID int, plan PlanV3) string {
	transformations := make([]string, 0, len(plan.Transformations))
	for _, transformation := range plan.Transformations {
		transformations = append(transformations, transformation.Executor+":"+transformation.Name+":"+transformation.RecipeVersion)
	}
	sort.Strings(transformations)
	parts := []string{
		attemptID,
		strconv.Itoa(requestedFileID),
		strconv.Itoa(effectiveFileID),
		string(plan.Delivery),
		string(plan.Stream.Protocol),
		strings.ToLower(plan.Stream.Container),
		strings.ToLower(plan.EffectiveRecipe.VideoCodec),
		strings.ToLower(plan.EffectiveRecipe.AudioCodec),
		optionalIntV3(plan.EffectiveRecipe.Width),
		optionalIntV3(plan.EffectiveRecipe.Height),
		optionalIntV3(plan.EffectiveRecipe.BitrateKbps),
		strings.ToLower(plan.EffectiveRecipe.DynamicRange),
		trackIdentityValueV3(plan.SelectedTracks.Audio),
		trackIdentityValueV3(plan.SelectedTracks.Subtitle),
		string(plan.Subtitle.Mode),
		strings.Join(transformations, ","),
	}
	parts = appendQuirkIdentityV3(parts, plan)
	parts = append(parts, PlanRecipeVersionV3)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "plan:" + hex.EncodeToString(sum[:16])
}

func PlanAttemptKeyV3(plan PlanV3, outputRouteGeneration int64, localMutations []string) string {
	transformations := make([]string, 0, len(plan.Transformations))
	for _, transformation := range plan.Transformations {
		transformations = append(transformations, transformation.Executor+":"+transformation.Name+":"+transformation.RecipeVersion)
	}
	sort.Strings(transformations)
	mutations := append([]string(nil), localMutations...)
	sort.Strings(mutations)
	parts := []string{
		plan.PlanID,
		plan.Delivery.KotlinName(),
		plan.Stream.Protocol.KotlinName(),
		strings.ToLower(plan.Stream.Container),
		strings.ToLower(plan.EffectiveRecipe.VideoCodec),
		strings.ToLower(plan.EffectiveRecipe.AudioCodec),
		optionalIntV3(plan.EffectiveRecipe.Width) + "x" + optionalIntV3(plan.EffectiveRecipe.Height),
		optionalIntV3(plan.EffectiveRecipe.BitrateKbps),
		strings.ToLower(plan.EffectiveRecipe.DynamicRange),
		plan.Subtitle.Mode.KotlinName(),
		strings.Join(transformations, ","),
	}
	parts = appendQuirkIdentityV3(parts, plan)
	parts = append(parts,
		strconv.FormatInt(outputRouteGeneration, 10),
		strings.Join(mutations, ","),
	)
	canonical := strings.Join(parts, "|")
	h := fnv.New64a()
	_, _ = h.Write([]byte(canonical))
	return fmt.Sprintf("v3:%016x", h.Sum64())
}

func appendQuirkIdentityV3(parts []string, plan PlanV3) []string {
	if len(plan.AppliedQuirks) == 0 && len(plan.RuntimeCorrections) == 0 {
		return parts
	}
	quirks := make([]string, 0, len(plan.AppliedQuirks))
	for _, quirk := range plan.AppliedQuirks {
		quirks = append(quirks, quirk.RegistryRevision+":"+quirk.ID)
	}
	runtimeCorrections := append([]string(nil), plan.RuntimeCorrections...)
	sort.Strings(quirks)
	sort.Strings(runtimeCorrections)
	return append(parts, strings.Join(quirks, ","), strings.Join(runtimeCorrections, ","))
}

func optionalIntV3(v *int) string {
	if v == nil {
		return "0"
	}
	return strconv.Itoa(*v)
}

func trackIdentityValueV3(v *TrackIdentityV3) string {
	if v == nil {
		return ""
	}
	return v.ID + ":" + optionalIntV3(v.Index)
}
