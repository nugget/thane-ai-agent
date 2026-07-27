package provenance

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// admissionKey is one identity a test can commit as: a private key on disk for
// git to sign with, and its public half for a seed declaration.
type admissionKey struct {
	principal string
	privPath  string
	publicKey string
}

func (k admissionKey) signer() TrustedSigner {
	return TrustedSigner{Principal: k.principal, PublicKey: k.publicKey}
}

func newAdmissionKey(t *testing.T, principal string) admissionKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate %s key: %v", principal, err)
	}
	sshPub, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("encode %s public key: %v", principal, err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, principal)
	if err != nil {
		t.Fatalf("marshal %s private key: %v", principal, err)
	}
	privPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write %s private key: %v", principal, err)
	}
	return admissionKey{
		principal: principal,
		privPath:  privPath,
		publicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))),
	}
}

// TestMain detaches every git invocation in this package from whatever
// configuration the developer running the tests happens to have — both the
// tests' own commits and the ones the code under test makes.
//
// These tests sign with git itself rather than through the Store, which is the
// point: admission has to judge history an operator committed by hand, not
// only history the agent wrote. But that means a global `gpg.ssh.program`
// applies, and the common one is 1Password's signer, which gets handed a test
// key it has never seen and refuses the commit. Verification reads the same
// setting, so a machine configured that way fails on both sides. Ambient
// `commit.gpgsign`, hooks, and commit templates are the same class of problem.
//
// Each test repository configures everything it needs locally, so the fix is
// to let nothing else in. Without it the suite passes or fails according to
// whose laptop it runs on, which is worse than either result on its own.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	os.Exit(m.Run())
}

// admissionRepo is a git repository tests drive directly, so a commit can be
// attributed to any key rather than only to the Store's own signer. Real roots
// acquire history both ways — the agent writes through the Store, an operator
// commits by hand — and admission has to judge the result either way.
type admissionRepo struct {
	t   *testing.T
	dir string
}

func newAdmissionRepo(t *testing.T) *admissionRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	r := &admissionRepo{t: t, dir: t.TempDir()}
	r.git("init", "-b", "main")
	r.git("config", "gpg.format", "ssh")
	r.git("config", "user.name", "Test")
	r.git("config", "user.email", "test@example.com")
	return r
}

func (r *admissionRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitTry runs git and returns its error instead of failing the test, for the
// cases where the exit status is itself what is being examined.
func (r *admissionRepo) gitTry(args ...string) error {
	r.t.Helper()
	return exec.Command("git", append([]string{"-C", r.dir}, args...)...).Run()
}

// commitAs writes files and commits them signed by the given key.
func (r *admissionRepo) commitAs(key admissionKey, message string, files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatalf("write %s: %v", name, err)
		}
	}
	r.git("add", "-A")
	r.git("-c", "user.signingkey="+key.privPath, "commit", "-S", "-m", message)
}

// trustFile renders an in-tree trust file listing the given keys. Its contents
// only matter to admission as a thing that changed; what admission judges is
// who signed the change.
func trustFile(keys ...admissionKey) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k.principal + " " + k.publicKey + "\n")
	}
	return b.String()
}

func TestVerifyAdmission(t *testing.T) {
	t.Parallel()

	operator := newAdmissionKey(t, "operator@example.com")
	agent := newAdmissionKey(t, AgentPrincipal)
	stranger := newAdmissionKey(t, "stranger@example.com")

	for _, tc := range []struct {
		name string
		// build lays down the repository's history.
		build func(r *admissionRepo)
		// seeds is what config declares for the root.
		seeds []TrustedSigner
		// want is a fragment of the expected refusal; empty means admitted.
		want string
	}{
		{
			name: "founded by a declared seed signer",
			build: func(r *admissionRepo) {
				r.commitAs(operator, "birth", map[string]string{
					TrustFileName: trustFile(operator),
					"mission.md":  "why we are here",
				})
			},
			seeds: []TrustedSigner{operator.signer()},
		},
		{
			name: "founded by the agent where the agent is a declared seed",
			build: func(r *admissionRepo) {
				r.commitAs(agent, "birth", map[string]string{
					TrustFileName: trustFile(agent),
				})
			},
			seeds: []TrustedSigner{agent.signer()},
		},
		{
			name: "founded by the agent where it is not declared",
			build: func(r *admissionRepo) {
				r.commitAs(agent, "birth", map[string]string{
					TrustFileName: trustFile(agent, operator),
				})
			},
			seeds: []TrustedSigner{operator.signer()},
			want:  "birth is unattributed",
		},
		{
			// The in-tree file trusting a key cannot be what makes that key
			// able to found the root — otherwise every root admits itself.
			name: "founded by a key the in-tree file trusts but config does not",
			build: func(r *admissionRepo) {
				r.commitAs(stranger, "birth", map[string]string{
					TrustFileName: trustFile(stranger, operator),
				})
			},
			seeds: []TrustedSigner{operator.signer()},
			want:  "birth is unattributed",
		},
		{
			name: "content commits from a delegated key are not admission's business",
			build: func(r *admissionRepo) {
				r.commitAs(operator, "birth", map[string]string{
					TrustFileName: trustFile(operator, stranger),
				})
				r.commitAs(stranger, "ordinary work", map[string]string{
					"notes.md": "delegated content",
				})
			},
			seeds: []TrustedSigner{operator.signer()},
		},
		{
			name: "trust file widened by a delegated key",
			build: func(r *admissionRepo) {
				r.commitAs(operator, "birth", map[string]string{
					TrustFileName: trustFile(operator, stranger),
				})
				r.commitAs(stranger, "add a friend", map[string]string{
					TrustFileName: trustFile(operator, stranger, agent),
				})
			},
			seeds: []TrustedSigner{operator.signer()},
			want:  "without a declared seed signer's signature",
		},
		{
			name: "trust file amended by a seed signer",
			build: func(r *admissionRepo) {
				r.commitAs(operator, "birth", map[string]string{
					TrustFileName: trustFile(operator),
				})
				r.commitAs(operator, "delegate to a collaborator", map[string]string{
					TrustFileName: trustFile(operator, stranger),
				})
			},
			seeds: []TrustedSigner{operator.signer()},
		},
		{
			name: "grafted history carrying a second birth",
			build: func(r *admissionRepo) {
				r.commitAs(operator, "birth", map[string]string{
					TrustFileName: trustFile(operator),
				})
				// An orphan branch is a second, independently born history;
				// merging it smuggles its commits in behind an admitted root.
				r.git("checkout", "--orphan", "smuggled")
				r.git("rm", "-rf", ".")
				r.commitAs(stranger, "unrelated birth", map[string]string{
					"payload.md": "arrived without admission",
				})
				r.git("checkout", "main")
				r.git("-c", "user.signingkey="+operator.privPath,
					"merge", "--allow-unrelated-histories", "--no-ff", "-S", "-m", "merge", "smuggled")
			},
			seeds: []TrustedSigner{operator.signer()},
			want:  "more than one birth",
		},
		{
			name: "no seed signers declared",
			build: func(r *admissionRepo) {
				r.commitAs(operator, "birth", map[string]string{
					TrustFileName: trustFile(operator),
				})
			},
			seeds: nil,
			want:  "no seed signers are declared",
		},
		{
			name:  "repository with no commits",
			build: func(r *admissionRepo) {},
			seeds: []TrustedSigner{operator.signer()},
			want:  "no commit history",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newAdmissionRepo(t)
			tc.build(repo)

			_, err := VerifyAdmission(t.Context(), repo.dir, tc.seeds)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("VerifyAdmission() = %v, want admitted", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("VerifyAdmission() = nil, want refusal containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("VerifyAdmission() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestVerifyAdmissionReportsWhatItChecked confirms the report carries the
// attribution, not just the verdict, so callers can log which commits the
// verdict rests on.
func TestVerifyAdmissionReportsWhatItChecked(t *testing.T) {
	t.Parallel()
	operator := newAdmissionKey(t, "operator@example.com")
	repo := newAdmissionRepo(t)
	repo.commitAs(operator, "birth", map[string]string{TrustFileName: trustFile(operator)})
	repo.commitAs(operator, "delegate", map[string]string{TrustFileName: trustFile(operator, operator)})

	report, err := VerifyAdmission(t.Context(), repo.dir, []TrustedSigner{operator.signer()})
	if err != nil {
		t.Fatalf("VerifyAdmission() = %v, want admitted", err)
	}
	if report.RootCommit == "" {
		t.Fatal("report.RootCommit is empty, want the parentless commit")
	}
	if len(report.TrustFileCommits) != 2 {
		t.Fatalf("report.TrustFileCommits = %v, want both commits that touched %s", report.TrustFileCommits, TrustFileName)
	}
}

// TestBootstrapBirthCommitRefusesToCreateAnInadmissibleRoot covers the state
// that would otherwise be unrecoverable: the agent founding a root whose seed
// set does not entitle it. Nothing committed later can fix a birth, so the
// refusal has to happen before the birth exists.
func TestBootstrapBirthCommitRefusesToCreateAnInadmissibleRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	operator := newAdmissionKey(t, "operator@example.com")

	store, err := NewWithOptions(t.TempDir(), testSigner(t), slog.New(slog.DiscardHandler), Options{
		SeedSigners: []TrustedSigner{operator.signer()},
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	err = store.BootstrapBirthCommit(t.Context())
	if err == nil {
		t.Fatal("BootstrapBirthCommit = nil, want refusal to found an inadmissible root")
	}
	if !strings.Contains(err.Error(), "could never be admitted") {
		t.Fatalf("BootstrapBirthCommit error = %v, want a born-inadmissible refusal", err)
	}
}

// TestBootstrapBirthCommitFoundsARootThatAdmitsTheAgent is the other half:
// declaring the agent is what makes an agent-founded root legitimate.
func TestBootstrapBirthCommitFoundsARootThatAdmitsTheAgent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	signer := testSigner(t)
	seeds := []TrustedSigner{{Principal: AgentPrincipal, PublicKey: signer.PublicKey()}}

	dir := t.TempDir()
	store, err := NewWithOptions(dir, signer, slog.New(slog.DiscardHandler), Options{SeedSigners: seeds})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if err := store.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	if _, err := VerifyAdmission(t.Context(), dir, seeds); err != nil {
		t.Fatalf("VerifyAdmission of a freshly founded root = %v, want admitted", err)
	}
}

// TestVerifyAdmissionIgnoresTheRepositoryOwnTrustFile is the regression guard
// for the mistake this whole check exists to prevent: a repository must not be
// able to make itself admissible by listing a key in its own trust file.
func TestVerifyAdmissionIgnoresTheRepositoryOwnTrustFile(t *testing.T) {
	t.Parallel()
	operator := newAdmissionKey(t, "operator@example.com")
	stranger := newAdmissionKey(t, "stranger@example.com")

	repo := newAdmissionRepo(t)
	repo.commitAs(stranger, "birth", map[string]string{
		// The strongest possible self-endorsement: the founder writes itself
		// into the trust file in the very commit being judged.
		TrustFileName: trustFile(stranger),
	})

	_, err := VerifyAdmission(t.Context(), repo.dir, []TrustedSigner{operator.signer()})
	if err == nil {
		t.Fatal("VerifyAdmission() = nil, want refusal: a root cannot admit itself")
	}
	if !strings.Contains(err.Error(), "birth is unattributed") {
		t.Fatalf("VerifyAdmission() error = %v, want unattributed-birth refusal", err)
	}
}

// TestVerifyAdmissionDistinguishesUnreadableFromUnborn keeps a directory git
// cannot read as a work tree from being reported as an empty history.
//
// The two need different repairs and only one of them involves history. An
// operator told "no commit history" about a root whose commits are intact and
// merely unreadable — wrong owner, wrong permissions, not a repository at all
// — is being pointed at a rewrite they must not perform.
func TestVerifyAdmissionDistinguishesUnreadableFromUnborn(t *testing.T) {
	t.Parallel()
	key := newAdmissionKey(t, "operator@example.com")

	t.Run("not a repository", func(t *testing.T) {
		t.Parallel()
		_, err := VerifyAdmission(t.Context(), t.TempDir(), []TrustedSigner{key.signer()})
		if err == nil {
			t.Fatal("VerifyAdmission() = nil, want a refusal for a non-repository")
		}
		if strings.Contains(err.Error(), "no commit history") {
			t.Fatalf("a directory that is not a repository must not be reported as an empty history: %v", err)
		}
		// git exits non-zero rather than printing "false" here, so the
		// wrapper carries git's own reason through instead of inventing one.
		if !strings.Contains(err.Error(), "read git repository") {
			t.Fatalf("VerifyAdmission() error = %v, want it to report a repository that could not be read", err)
		}
	})

	t.Run("repository with no commits", func(t *testing.T) {
		t.Parallel()
		repo := newAdmissionRepo(t)
		_, err := VerifyAdmission(t.Context(), repo.dir, []TrustedSigner{key.signer()})
		if err == nil || !strings.Contains(err.Error(), "no commit history") {
			t.Fatalf("VerifyAdmission() error = %v, want it to report no commit history", err)
		}
	})
}

// swapSignersProgram is a gpg.ssh.program that delegates to the real
// ssh-keygen but substitutes its own allowed-signers file. Rotating the
// positional parameters preserves each argument exactly, so a path containing
// spaces survives; only the value after -f is replaced.
const swapSignersProgram = `#!/bin/sh
swap=0
n=$#
i=0
while [ $i -lt $n ]; do
	a="$1"; shift
	if [ "$swap" = 1 ]; then set -- "$@" '%s'; swap=0
	elif [ "$a" = "-f" ]; then set -- "$@" "-f"; swap=1
	else set -- "$@" "$a"; fi
	i=$((i + 1))
done
exec ssh-keygen "$@"
`

// TestAdmissionIgnoresRepositoryConfiguredSignatureProgram proves the trust
// decision cannot be reassigned by editing configuration.
//
// `gpg.ssh.program` names the executable git asks whether a signature is good,
// and a repository's own .git/config can set it. That file lives inside the
// root being judged, writable by anything with filesystem access — including
// this agent wherever shell access is enabled. If admission honored it, a root
// could nominate its own judge, which is the same self-vouching that admission
// exists to refuse, moved one level down.
//
// The attack is the one that actually works: rather than forging ssh-keygen's
// output, the configured program delegates to the real one and swaps the
// allowed-signers file for its own, so git reports a genuinely good signature
// by a key it was handed.
//
// The two control assertions are the point of the test rather than decoration.
// Admission refusing proves nothing on its own — it would refuse just as
// readily if the wrapper were broken, /bin/sh were missing, or git had stopped
// honoring the setting. So the test first shows the honest check refusing this
// commit, then shows the wrapper turning that same check into a pass. Only
// then does refusal by admission mean the pin is what did it.
func TestAdmissionIgnoresRepositoryConfiguredSignatureProgram(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	operator := newAdmissionKey(t, "operator@example.com")
	stranger := newAdmissionKey(t, "stranger@example.com")

	repo := newAdmissionRepo(t)
	repo.commitAs(stranger, "birth", map[string]string{
		TrustFileName: trustFile(stranger, operator),
	})

	dir := t.TempDir()
	honest := filepath.Join(dir, "honest_allowed")
	if err := os.WriteFile(honest, []byte(operator.principal+" "+operator.publicKey+"\n"), 0o644); err != nil {
		t.Fatalf("write honest signers: %v", err)
	}
	attacker := filepath.Join(dir, "attacker_allowed")
	if err := os.WriteFile(attacker, []byte(stranger.principal+" "+stranger.publicKey+"\n"), 0o644); err != nil {
		t.Fatalf("write attacker signers: %v", err)
	}
	program := filepath.Join(dir, "swap-signers.sh")
	if err := os.WriteFile(program, fmt.Appendf(nil, swapSignersProgram, attacker), 0o755); err != nil {
		t.Fatalf("write program: %v", err)
	}

	// Control: honestly checked, this birth is signed by a key the trust set
	// does not name, and git refuses it.
	if err := repo.gitTry("-c", "gpg.ssh.allowedSignersFile="+honest, "verify-commit", "HEAD"); err == nil {
		t.Fatal("control failed: an untrusted birth verified without the wrapper")
	}
	// Control: the wrapper turns that refusal into a pass, so the bypass this
	// test guards against is live and the assertion below can attribute a
	// refusal to the pin rather than to a broken fixture.
	if err := repo.gitTry("-c", "gpg.ssh.allowedSignersFile="+honest,
		"-c", "gpg.ssh.program="+program, "verify-commit", "HEAD"); err != nil {
		t.Fatalf("control failed: the wrapper did not forge a passing verification, so this test proves nothing: %v", err)
	}

	repo.git("config", "gpg.ssh.program", program)
	if _, err := VerifyAdmission(t.Context(), repo.dir, []TrustedSigner{operator.signer()}); err == nil {
		t.Fatal("a root that configured its own signature program was admitted")
	}
}
