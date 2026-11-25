package backup

import (
	"context"
	"fmt"
)

// deployment is what checkDeployment decided about a full restore: whether
// the backup is this deployment's, and if not, which server identity
// (serveridentity.Key) the restored server answers with.
type deployment struct {
	// same is true when the backup's authenticated manifest names this
	// deployment's server id.
	same bool
	// takesBackupIdentity is true for another deployment's backup restored
	// onto a server with no accounts: an install set up to receive it, as
	// when a server moves to new hardware. The restored server keeps the
	// backup's server id, which every signed app key request names, so the
	// backup's app keys keep working. Every other server-specific setting
	// still comes from the live server. A server with an account keeps its
	// own id: someone already uses it as this server.
	takesBackupIdentity bool
}

// What the restore output says about the server identity of another
// deployment's backup.
const (
	shutDownOriginal = "Shut down the server the backup came from before you restore, and keep it off: this server will answer as that one, " +
		"two servers that answer as one confuse the apps, and an app key revoked on one keeps working on the other."
	keepOriginalOff = "Keep the server the backup came from shut down: two servers that answer as one confuse the apps, " +
		"and an app key revoked on one keeps working on the other."
	identityTaken = "This server now answers as the server the backup came from, so devices that signed in to it with an app key can sign in here."
	identityKept  = "This server kept its own identity. App keys from the backup will not work here: " +
		"restore onto an install where no account has been created to keep them working."
)

// appKeys is what the restore does with the backup's app keys: every signed
// request names the server id, so the keys keep working where the restored
// server answers with the backup's, and are revoked where it keeps its own.
func (d deployment) appKeys() AppKeyCarry {
	if d.same || d.takesBackupIdentity {
		return AppKeysKept
	}
	return AppKeysRevoked
}

// notice says, before anything changes, which identity the restored
// server will answer with.
func (d deployment) notice() string {
	if d.takesBackupIdentity {
		return "This server has no accounts, so the restore gives it the identity of the server the backup came from: " +
			"devices that signed in there with an app key can sign in here."
	}
	return "This server has accounts, so it keeps its own identity. App keys from the backup will not work here: " +
		"to keep them working, stop now and restore onto an install where no account has been created."
}

// recheck confirms, once the live server is idle, that it still has no
// accounts when the restore takes the backup's identity: one created since
// checkDeployment read it would be someone using this server as itself.
func (d deployment) recheck(ctx context.Context, loader DatabaseLoader, liveURL string) error {
	if !d.takesBackupIdentity {
		return nil
	}
	accounts, err := loader.HasAccounts(ctx, liveURL)
	if err != nil {
		return fmt.Errorf("reading whether this server has accounts: %w", err)
	}
	if accounts {
		return fmt.Errorf("%w: an account was created on this server after the restore began, so it would no longer take the backup's identity. Stop Silo on every server and run the restore again", ErrLiveInUse)
	}
	return nil
}
