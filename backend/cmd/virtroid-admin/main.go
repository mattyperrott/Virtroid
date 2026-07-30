package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/store"
)

const (
	defaultRotationOverlap     = 15 * time.Minute
	defaultBootstrapInviteTTL  = 30 * time.Minute
	minimumBootstrapInviteTTL  = time.Minute
	maximumBootstrapInviteTTL  = 7 * 24 * time.Hour
	bootstrapInviteRandomBytes = 32
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "virtroid-admin: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("command is required: approve, bootstrap-invite, list, operator-approve, operator-revoke, public-key, revoke, or rotate")
	}
	// Public-key derivation intentionally has no database dependency. This lets
	// an operator inspect a candidate image against the node's existing private
	// key before the registry schema or control plane is deployed.
	if args[0] == "public-key" {
		return runPublicKey(args[1:], stdout, stderr)
	}
	command, err := prepareDatabaseCommand(args, stderr)
	if err != nil {
		return err
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	registry, err := store.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer registry.Close()
	if err := registry.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}

	return command(ctx, registry, stdout)
}

type databaseCommand func(context.Context, *store.Store, io.Writer) error

func prepareDatabaseCommand(args []string, stderr io.Writer) (databaseCommand, error) {
	switch args[0] {
	case "approve":
		return prepareApprove(args[1:], stderr)
	case "bootstrap-invite":
		return prepareBootstrapInvite(args[1:], stderr)
	case "list":
		return prepareList(args[1:], stderr)
	case "operator-approve":
		return prepareOperatorApprove(args[1:], stderr)
	case "operator-revoke":
		return prepareOperatorRevoke(args[1:], stderr)
	case "revoke":
		return prepareRevoke(args[1:], stderr)
	case "rotate":
		return prepareRotate(args[1:], stderr)
	default:
		return nil, fmt.Errorf("unsupported command %q: use approve, bootstrap-invite, list, operator-approve, operator-revoke, public-key, revoke, or rotate", args[0])
	}
}

func prepareBootstrapInvite(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("bootstrap-invite", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ttl := flags.Duration("ttl", defaultBootstrapInviteTTL, "validity window between 1m and 168h")
	label := flags.String("label", "", "optional device or recipient label")
	actor := flags.String("actor", defaultActor(), "operator creating the invitation")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("bootstrap-invite does not accept positional arguments")
	}
	if *ttl < minimumBootstrapInviteTTL || *ttl > maximumBootstrapInviteTTL {
		return nil, fmt.Errorf("bootstrap-invite --ttl must be between %s and %s", minimumBootstrapInviteTTL, maximumBootstrapInviteTTL)
	}
	normalizedLabel := strings.TrimSpace(*label)
	normalizedActor := strings.TrimSpace(*actor)
	if normalizedActor == "" {
		return nil, errors.New("bootstrap-invite --actor is required")
	}
	if len([]rune(normalizedActor)) > 128 {
		return nil, errors.New("bootstrap-invite --actor is too long")
	}
	if len([]rune(normalizedLabel)) > 128 {
		return nil, errors.New("bootstrap-invite --label is too long")
	}

	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		token, tokenSHA256, err := generateBootstrapInviteToken()
		if err != nil {
			return fmt.Errorf("generate bootstrap invitation: %w", err)
		}
		invitation, err := registry.CreateBootstrapInvitation(
			ctx,
			tokenSHA256,
			normalizedLabel,
			normalizedActor,
			time.Now().UTC().Add(*ttl),
		)
		if err != nil {
			return fmt.Errorf("create bootstrap invitation: %w", err)
		}
		result := struct {
			InviteToken string                    `json:"invite_token"`
			Invitation  store.BootstrapInvitation `json:"invitation"`
		}{
			InviteToken: token,
			Invitation:  invitation,
		}
		if *jsonOutput {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		fmt.Fprintf(
			stdout,
			"invite_token=%s\ninvite_id=%s\nexpires_at=%s\nlabel=%s\ncreated_by=%s\n",
			result.InviteToken,
			result.Invitation.ID,
			result.Invitation.ExpiresAt.UTC().Format(time.RFC3339),
			result.Invitation.Label,
			result.Invitation.CreatedBy,
		)
		return nil
	}, nil
}

func generateBootstrapInviteToken() (string, []byte, error) {
	randomBytes := make([]byte, bootstrapInviteRandomBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes)
	tokenSHA256 := sha256.Sum256([]byte(token))
	return token, tokenSHA256[:], nil
}

func runPublicKey(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("public-key", flag.ContinueOnError)
	flags.SetOutput(stderr)
	privateKeyEnv := flags.String("private-key-env", "", "environment variable containing the private key (defaults to NODE_PRIVATE_KEY_B64)")
	privateKeyFile := flags.String("private-key-file", "", "file containing a PKCS#8 or SEC1 P-256 private key")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("public-key does not accept positional arguments")
	}
	material, source, err := readPrivateKey(*privateKeyEnv, *privateKeyFile)
	if err != nil {
		return err
	}
	privateKey, publicKey, err := nodeauth.LoadPrivateKey(material)
	if err != nil {
		return fmt.Errorf("load private key from %s: %w", source, err)
	}
	if privateKey == nil {
		return fmt.Errorf("load private key from %s: %w", source, nodeauth.ErrMissingPrivateKey)
	}
	canonicalPublicKey, fingerprint, err := nodeauth.NormalizePublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("derive P-256 public key from %s: %w", source, err)
	}

	result := struct {
		PublicKeyB64      string `json:"public_key_b64"`
		FingerprintSHA256 string `json:"fingerprint_sha256"`
	}{
		PublicKeyB64:      canonicalPublicKey,
		FingerprintSHA256: fingerprint,
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintf(stdout, "public_key_b64=%s\nfingerprint_sha256=%s\n", result.PublicKeyB64, result.FingerprintSHA256)
	return nil
}

func prepareApprove(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("approve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nodeID := flags.String("node", "", "stable node ID")
	operatorID := flags.String("operator", "local", "approved operator ID")
	operatorName := flags.String("operator-name", "Local trusted operator", "operator display name")
	publicKey := flags.String("public-key", "", "P-256 public key as PEM or base64 PKIX")
	publicKeyFile := flags.String("public-key-file", "", "file containing the P-256 public key")
	actor := flags.String("actor", defaultActor(), "audit actor")
	reason := flags.String("reason", "initial node approval", "audit reason")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("approve does not accept positional arguments")
	}
	keyMaterial, err := readPublicKey(*publicKey, *publicKeyFile)
	if err != nil {
		return nil, err
	}
	input := store.ApproveNodeInput{
		NodeID:       *nodeID,
		OperatorID:   *operatorID,
		OperatorName: *operatorName,
		PublicKey:    keyMaterial,
		Actor:        *actor,
		Reason:       *reason,
	}
	if err := store.ValidateApproveNodeInput(input); err != nil {
		return nil, fmt.Errorf("validate approval: %w", err)
	}
	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		node, err := registry.ApproveNode(ctx, input)
		if err != nil {
			return fmt.Errorf("approve node: %w", err)
		}
		printNodeSummary(stdout, "approved", node)
		return nil
	}, nil
}

func prepareOperatorApprove(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("operator-approve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operatorID := flags.String("operator", "", "stable operator ID")
	name := flags.String("name", "", "operator display name (defaults to the operator ID)")
	actor := flags.String("actor", defaultActor(), "audit actor")
	reason := flags.String("reason", "explicit operator approval", "required audit reason")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("operator-approve does not accept positional arguments")
	}
	input := store.ApproveOperatorInput{
		OperatorID: *operatorID,
		Name:       *name,
		Actor:      *actor,
		Reason:     *reason,
	}
	if err := store.ValidateApproveOperatorInput(input); err != nil {
		return nil, fmt.Errorf("validate operator approval: %w", err)
	}
	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		operator, err := registry.ApproveOperator(ctx, input)
		if err != nil {
			return fmt.Errorf("approve operator: %w", err)
		}
		printOperatorSummary(stdout, "approved", operator)
		return nil
	}, nil
}

func prepareOperatorRevoke(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("operator-revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operatorID := flags.String("operator", "", "stable operator ID")
	actor := flags.String("actor", defaultActor(), "audit actor")
	reason := flags.String("reason", "", "required revocation reason")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("operator-revoke does not accept positional arguments")
	}
	input := store.RevokeOperatorInput{
		OperatorID: *operatorID,
		Actor:      *actor,
		Reason:     *reason,
	}
	if err := store.ValidateRevokeOperatorInput(input); err != nil {
		return nil, fmt.Errorf("validate operator revocation: %w", err)
	}
	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		operator, err := registry.RevokeOperator(ctx, input)
		if err != nil {
			return fmt.Errorf("revoke operator: %w", err)
		}
		printOperatorSummary(stdout, "revoked", operator)
		return nil
	}, nil
}

func prepareList(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nodeID := flags.String("node", "", "optional node ID")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("list does not accept positional arguments")
	}
	normalizedNodeID := strings.TrimSpace(*nodeID)
	if normalizedNodeID != "" {
		if err := store.ValidateNodeRegistryID(normalizedNodeID); err != nil {
			return nil, fmt.Errorf("validate node id: %w", err)
		}
	}
	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		var nodes []store.ApprovedNode
		if normalizedNodeID != "" {
			node, err := registry.ApprovedNode(ctx, normalizedNodeID)
			if err != nil {
				return fmt.Errorf("get approved node: %w", err)
			}
			nodes = []store.ApprovedNode{node}
		} else {
			var err error
			nodes, err = registry.ListApprovedNodes(ctx)
			if err != nil {
				return fmt.Errorf("list approved nodes: %w", err)
			}
		}
		if *jsonOutput {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(nodes)
		}
		for _, node := range nodes {
			printNodeSummary(stdout, node.Status, node)
			for _, key := range node.Keys {
				fmt.Fprintf(
					stdout,
					"  key_version=%d state=%s fingerprint_sha256=%s valid_from=%s valid_until=%s retired_at=%s created_at=%s\n",
					key.KeyVersion,
					key.State,
					key.FingerprintSHA256,
					key.ValidFrom.UTC().Format(time.RFC3339),
					formatOptionalTime(key.ValidUntil),
					formatOptionalTime(key.RetiredAt),
					key.CreatedAt.UTC().Format(time.RFC3339),
				)
			}
		}
		return nil
	}, nil
}

func prepareRevoke(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nodeID := flags.String("node", "", "stable node ID")
	actor := flags.String("actor", defaultActor(), "audit actor")
	reason := flags.String("reason", "", "required revocation reason")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("revoke does not accept positional arguments")
	}
	input := store.RevokeNodeInput{
		NodeID: *nodeID,
		Actor:  *actor,
		Reason: *reason,
	}
	if err := store.ValidateRevokeNodeInput(input); err != nil {
		return nil, fmt.Errorf("validate revocation: %w", err)
	}
	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		node, err := registry.RevokeNode(ctx, input)
		if err != nil {
			return fmt.Errorf("revoke node: %w", err)
		}
		printNodeSummary(stdout, "revoked", node)
		return nil
	}, nil
}

func prepareRotate(args []string, stderr io.Writer) (databaseCommand, error) {
	flags := flag.NewFlagSet("rotate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nodeID := flags.String("node", "", "stable node ID")
	publicKey := flags.String("public-key", "", "new P-256 public key as PEM or base64 PKIX")
	publicKeyFile := flags.String("public-key-file", "", "file containing the new P-256 public key")
	overlap := flags.Duration("overlap", defaultRotationOverlap, "old-key overlap, maximum 24h")
	actor := flags.String("actor", defaultActor(), "audit actor")
	reason := flags.String("reason", "scheduled node key rotation", "audit reason")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("rotate does not accept positional arguments")
	}
	keyMaterial, err := readPublicKey(*publicKey, *publicKeyFile)
	if err != nil {
		return nil, err
	}
	input := store.RotateNodeKeyInput{
		NodeID:    *nodeID,
		PublicKey: keyMaterial,
		Overlap:   *overlap,
		Actor:     *actor,
		Reason:    *reason,
	}
	if err := store.ValidateRotateNodeKeyInput(input); err != nil {
		return nil, fmt.Errorf("validate rotation: %w", err)
	}
	return func(ctx context.Context, registry *store.Store, stdout io.Writer) error {
		node, err := registry.RotateNodeKey(ctx, input)
		if err != nil {
			return fmt.Errorf("rotate node key: %w", err)
		}
		printNodeSummary(stdout, "rotated", node)
		return nil
	}, nil
}

func readPublicKey(inline, path string) (string, error) {
	inline = strings.TrimSpace(inline)
	path = strings.TrimSpace(path)
	if (inline == "") == (path == "") {
		return "", errors.New("exactly one of --public-key or --public-key-file is required")
	}
	if inline != "" {
		return inline, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read public key file: %w", err)
	}
	material := strings.TrimSpace(string(payload))
	if material == "" {
		return "", errors.New("public key file is empty")
	}
	return material, nil
}

func readPrivateKey(environmentName, path string) (string, string, error) {
	environmentName = strings.TrimSpace(environmentName)
	path = strings.TrimSpace(path)
	if environmentName != "" && path != "" {
		return "", "", errors.New("at most one of --private-key-env or --private-key-file may be set")
	}
	if path != "" {
		payload, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("read private key file: %w", err)
		}
		material := strings.TrimSpace(string(payload))
		if material == "" {
			return "", "", errors.New("private key file is empty")
		}
		return material, "file " + path, nil
	}
	if environmentName == "" {
		environmentName = "NODE_PRIVATE_KEY_B64"
	}
	if !validEnvironmentName(environmentName) {
		return "", "", errors.New("--private-key-env must be a valid environment variable name")
	}
	material := strings.TrimSpace(os.Getenv(environmentName))
	if material == "" {
		return "", "", fmt.Errorf("private key environment variable %s is empty or unset", environmentName)
	}
	return material, "environment " + environmentName, nil
}

func validEnvironmentName(name string) bool {
	for index, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return name != ""
}

func defaultActor() string {
	for _, name := range []string{"SUDO_USER", "USER"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return "virtroid-admin"
}

func printNodeSummary(output io.Writer, action string, node store.ApprovedNode) {
	fingerprint := ""
	for _, key := range node.Keys {
		if key.KeyVersion == node.ActiveKeyVersion {
			fingerprint = key.FingerprintSHA256
			break
		}
	}
	fmt.Fprintf(
		output,
		"%s node=%s operator=%s status=%s active_key_version=%d fingerprint_sha256=%s approved_at=%s revoked_at=%s\n",
		action,
		node.NodeID,
		node.OperatorID,
		node.Status,
		node.ActiveKeyVersion,
		fingerprint,
		node.ApprovedAt.UTC().Format(time.RFC3339),
		formatOptionalTime(node.RevokedAt),
	)
}

func printOperatorSummary(output io.Writer, action string, operator store.NodeOperator) {
	fmt.Fprintf(
		output,
		"%s operator=%s name=%q status=%s approved_at=%s revoked_at=%s\n",
		action,
		operator.ID,
		operator.Name,
		operator.Status,
		operator.ApprovedAt.UTC().Format(time.RFC3339),
		formatOptionalTime(operator.RevokedAt),
	)
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
