package modules

import (
	"fmt"
	"path/filepath"
	"sync"

	"go.uber.org/fx"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/actors"
	"github.com/filecoin-project/lotus/node/bundle"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
	"github.com/filecoin-project/lotus/node/modules/helpers"
	"github.com/filecoin-project/lotus/node/repo"

	cid "github.com/ipfs/go-cid"
	dstore "github.com/ipfs/go-datastore"
	cbor "github.com/ipfs/go-ipld-cbor"
)

func LoadBuiltinActors(lc fx.Lifecycle, mctx helpers.MetricsCtx, r repo.LockedRepo, bs dtypes.UniversalBlockstore, ds dtypes.MetadataDS) (result dtypes.BuiltinActorsLoaded, err error) {
	ctx := helpers.LifecycleCtx(mctx, lc)

	// We can't put it as a dep in inputs causes a stack overflow in DI from circular dependency
	// So we pass it through ldflags instead
	netw := build.NetworkBundle
	if netw == "" {
		netw = "mainnet"
	}

	for av, bd := range build.BuiltinActorReleases {
		// first check to see if we know this release
		key := dstore.NewKey(fmt.Sprintf("/builtin-actors/v%d/%s", av, bd.Release))

		data, err := ds.Get(ctx, key)
		switch err {
		case nil:
			// ok, we do, this should be the manifest cid
			mfCid, err := cid.Cast(data)
			if err != nil {
				return result, xerrors.Errorf("error parsing cid for %s: %w", key, err)
			}

			// check the blockstore for existence of the manifest
			has, err := bs.Has(ctx, mfCid)
			if err != nil {
				return result, xerrors.Errorf("error checking blockstore for manifest cid %s: %w", mfCid, err)
			}

			if has {
				// it's there, no need to reload the bundle to the blockstore; just add it to the manifest list.
				actors.AddManifest(av, mfCid)
				continue
			}

			// we have a release key but don't have the manifest in the blockstore; maybe the user
			// nuked his blockstore to restart from a snapshot. So fallthrough to refetch (if necessary)
			// and reload the bundle.

		case dstore.ErrNotFound:
			// we don't have a release key, we need to load the bundle

		default:
			return result, xerrors.Errorf("error loading %s from datastore: %w", key, err)
		}

		// we haven't recorded it in the datastore, so we need to load it
		var mfCid cid.Cid
		if bd.Path != "" {
			// this is a local bundle, load it directly from the filessystem
			mfCid, err = bundle.LoadBundle(ctx, bs, bd.Path, av)
			if err != nil {
				return result, err
			}
		} else {
			// fetch it and add it to the blockstore
			mfCid, err = bundle.FetchAndLoadBundle(ctx, r.Path(), bs, av, bd.Release, netw)
			if err != nil {
				return result, err
			}
		}

		if bd.Development {
			// don't store the release key so that we always load development bundles
			continue
		}

		// add the release key with the manifest to avoid reloading it in next restart.
		if err := ds.Put(ctx, key, mfCid.Bytes()); err != nil {
			return result, xerrors.Errorf("error storing manifest CID for builtin-actors vrsion %d to the datastore: %w", av, err)
		}
	}

	// we've loaded all the bundles, now load the manifests to get actor code CIDs.
	cborStore := cbor.NewCborStore(bs)
	if err := actors.LoadManifests(ctx, cborStore); err != nil {
		return result, xerrors.Errorf("error loading actor manifests: %w", err)
	}

	return result, nil
}

// for itests
var testingBundleMx sync.Mutex

func LoadBuiltinActorsTesting(lc fx.Lifecycle, mctx helpers.MetricsCtx, bs dtypes.UniversalBlockstore) (result dtypes.BuiltinActorsLoaded, err error) {
	ctx := helpers.LifecycleCtx(mctx, lc)

	var netw string
	if build.InsecurePoStValidation {
		netw = "testing-fake-proofs"
	} else {
		netw = "testing"
	}

	testingBundleMx.Lock()
	defer testingBundleMx.Unlock()

	for av, bd := range build.BuiltinActorReleases {
		if bd.Path != "" {
			// we need the appropriate bundle for tests; it should live next to the main bundle, with the
			// appropriate network name
			path := filepath.Join(filepath.Dir(bd.Path), fmt.Sprintf("builtin-actors-%s.car", netw))
			if _, err := bundle.LoadBundle(ctx, bs, path, av); err != nil {
				return result, xerrors.Errorf("error loading testing bundle for builtin-actors version %d/%s: %w", av, netw, err)
			}
		} else {
			const basePath = "/tmp/lotus-testing"
			if _, err := bundle.FetchAndLoadBundle(ctx, basePath, bs, av, bd.Release, netw); err != nil {
				return result, xerrors.Errorf("error loading testing bundle for builtin-actors version %d/%s: %w", av, netw, err)
			}
		}
	}

	cborStore := cbor.NewCborStore(bs)
	if err := actors.LoadManifests(ctx, cborStore); err != nil {
		return result, xerrors.Errorf("error loading actor manifests: %w", err)
	}

	return result, nil
}
