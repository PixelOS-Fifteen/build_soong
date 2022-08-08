// Copyright 2018 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package java

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/blueprint"

	"android/soong/android"
	"android/soong/dexpreopt"
)

var manifestFixerRule = pctx.AndroidStaticRule("manifestFixer",
	blueprint.RuleParams{
		Command: `${config.ManifestFixerCmd} ` +
			`$args $in $out`,
		CommandDeps: []string{"${config.ManifestFixerCmd}"},
	},
	"args")

var manifestMergerRule = pctx.AndroidStaticRule("manifestMerger",
	blueprint.RuleParams{
		Command:     `${config.ManifestMergerCmd} $args --main $in $libs --out $out`,
		CommandDeps: []string{"${config.ManifestMergerCmd}"},
	},
	"args", "libs")

// targetSdkVersion for manifest_fixer
// When TARGET_BUILD_APPS is not empty, this method returns 10000 for modules targeting an unreleased SDK
// This enables release builds (that run with TARGET_BUILD_APPS=[val...]) to target APIs that have not yet been finalized as part of an SDK
func targetSdkVersionForManifestFixer(ctx android.ModuleContext, sdkContext android.SdkContext) string {
	targetSdkVersionSpec := sdkContext.TargetSdkVersion(ctx)
	// Return 10000 for modules targeting "current" if either
	// 1. The module is built in unbundled mode (TARGET_BUILD_APPS not empty)
	// 2. The module is run as part of MTS, and should be testable on stable branches
	// TODO(b/240294501): Determine the rules for handling test apexes
	if targetSdkVersionSpec.ApiLevel.IsPreview() && (ctx.Config().UnbundledBuildApps() || includedInMts(ctx.Module())) {
		return strconv.Itoa(android.FutureApiLevel.FinalOrFutureInt())
	}
	targetSdkVersion, err := targetSdkVersionSpec.EffectiveVersionString(ctx)
	if err != nil {
		ctx.ModuleErrorf("invalid targetSdkVersion: %s", err)
	}
	return targetSdkVersion
}

// Helper function that casts android.Module to java.androidTestApp
// If this type conversion is possible, it queries whether the test app is included in an MTS suite
func includedInMts(module android.Module) bool {
	if test, ok := module.(androidTestApp); ok {
		return test.includedInTestSuite("mts")
	}
	return false
}

type ManifestFixerParams struct {
	SdkContext            android.SdkContext
	ClassLoaderContexts   dexpreopt.ClassLoaderContextMap
	IsLibrary             bool
	UseEmbeddedNativeLibs bool
	UsesNonSdkApis        bool
	UseEmbeddedDex        bool
	HasNoCode             bool
	TestOnly              bool
	LoggingParent         string
}

// Uses manifest_fixer.py to inject minSdkVersion, etc. into an AndroidManifest.xml
func ManifestFixer(ctx android.ModuleContext, manifest android.Path,
	params ManifestFixerParams) android.Path {
	var args []string

	if params.IsLibrary {
		args = append(args, "--library")
	} else if params.SdkContext != nil {
		minSdkVersion, err := params.SdkContext.MinSdkVersion(ctx).EffectiveVersion(ctx)
		if err != nil {
			ctx.ModuleErrorf("invalid minSdkVersion: %s", err)
		}
		if minSdkVersion.FinalOrFutureInt() >= 23 {
			args = append(args, fmt.Sprintf("--extract-native-libs=%v", !params.UseEmbeddedNativeLibs))
		} else if params.UseEmbeddedNativeLibs {
			ctx.ModuleErrorf("module attempted to store uncompressed native libraries, but minSdkVersion=%d doesn't support it",
				minSdkVersion)
		}
	}

	if params.UsesNonSdkApis {
		args = append(args, "--uses-non-sdk-api")
	}

	if params.UseEmbeddedDex {
		args = append(args, "--use-embedded-dex")
	}

	if params.ClassLoaderContexts != nil {
		// manifest_fixer should add only the implicit SDK libraries inferred by Soong, not those added
		// explicitly via `uses_libs`/`optional_uses_libs`.
		requiredUsesLibs, optionalUsesLibs := params.ClassLoaderContexts.ImplicitUsesLibs()

		for _, usesLib := range requiredUsesLibs {
			args = append(args, "--uses-library", usesLib)
		}
		for _, usesLib := range optionalUsesLibs {
			args = append(args, "--optional-uses-library", usesLib)
		}
	}

	if params.HasNoCode {
		args = append(args, "--has-no-code")
	}

	if params.TestOnly {
		args = append(args, "--test-only")
	}

	if params.LoggingParent != "" {
		args = append(args, "--logging-parent", params.LoggingParent)
	}
	var deps android.Paths
	var argsMapper = make(map[string]string)

	if params.SdkContext != nil {
		targetSdkVersion := targetSdkVersionForManifestFixer(ctx, params.SdkContext)
		args = append(args, "--targetSdkVersion ", targetSdkVersion)

		if UseApiFingerprint(ctx) && ctx.ModuleName() != "framework-res" {
			targetSdkVersion = ctx.Config().PlatformSdkCodename() + fmt.Sprintf(".$$(cat %s)", ApiFingerprintPath(ctx).String())
			deps = append(deps, ApiFingerprintPath(ctx))
		}

		minSdkVersion, err := params.SdkContext.MinSdkVersion(ctx).EffectiveVersionString(ctx)
		if err != nil {
			ctx.ModuleErrorf("invalid minSdkVersion: %s", err)
		}

		if UseApiFingerprint(ctx) && ctx.ModuleName() != "framework-res" {
			minSdkVersion = ctx.Config().PlatformSdkCodename() + fmt.Sprintf(".$$(cat %s)", ApiFingerprintPath(ctx).String())
			deps = append(deps, ApiFingerprintPath(ctx))
		}

		if err != nil {
			ctx.ModuleErrorf("invalid minSdkVersion: %s", err)
		}
		args = append(args, "--minSdkVersion ", minSdkVersion)
		args = append(args, "--raise-min-sdk-version")
	}

	fixedManifest := android.PathForModuleOut(ctx, "manifest_fixer", "AndroidManifest.xml")
	argsMapper["args"] = strings.Join(args, " ")

	ctx.Build(pctx, android.BuildParams{
		Rule:        manifestFixerRule,
		Description: "fix manifest",
		Input:       manifest,
		Implicits:   deps,
		Output:      fixedManifest,
		Args:        argsMapper,
	})

	return fixedManifest.WithoutRel()
}

func manifestMerger(ctx android.ModuleContext, manifest android.Path, staticLibManifests android.Paths,
	isLibrary bool) android.Path {

	var args string
	if !isLibrary {
		// Follow Gradle's behavior, only pass --remove-tools-declarations when merging app manifests.
		args = "--remove-tools-declarations"
	}

	mergedManifest := android.PathForModuleOut(ctx, "manifest_merger", "AndroidManifest.xml")
	ctx.Build(pctx, android.BuildParams{
		Rule:        manifestMergerRule,
		Description: "merge manifest",
		Input:       manifest,
		Implicits:   staticLibManifests,
		Output:      mergedManifest,
		Args: map[string]string{
			"libs": android.JoinWithPrefix(staticLibManifests.Strings(), "--libs "),
			"args": args,
		},
	})

	return mergedManifest.WithoutRel()
}
