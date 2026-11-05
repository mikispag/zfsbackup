#!/usr/bin/env bash
# Generates Markdown release notes for VERSION: a changelog of the commits
# since the previous release, grouped by Conventional Commits type, followed
# by download and verification instructions for the archives in DIST_DIR.
#
# Usage: release-notes.sh VERSION DIST_DIR
# Run from the repository root with full history and tags available.
# shellcheck disable=SC2016 # backticks in printf formats are Markdown.
set -euo pipefail

version=${1:?usage: release-notes.sh VERSION DIST_DIR}
dist=${2:?usage: release-notes.sh VERSION DIST_DIR}
server=${GITHUB_SERVER_URL:-https://github.com}
repo=${GITHUB_REPOSITORY:-mikispag/zfsbackup}
repo_url="$server/$repo"

# A stable release lists everything since the previous stable release; a
# pre-release lists everything since the previous tag of any kind.
describe_args=(--tags --abbrev=0 --match 'v[0-9]*')
if [[ $version != *-* ]]; then
  describe_args+=(--exclude '*-*')
fi
previous=$(git describe "${describe_args[@]}" HEAD 2>/dev/null || true)
range=HEAD
if [[ -n $previous ]]; then
  range="$previous..HEAD"
fi

declare -A sections=()
breaking=""

while IFS=$'\x1f' read -r -d $'\x1e' sha subject body; do
  sha=${sha#$'\n'}
  [[ -n $sha ]] || continue
  type="" scope="" bang="" text=$subject
  if [[ $subject =~ ^([A-Za-z]+)(\(([^\)]+)\))?(!)?:[[:space:]]+(.+)$ ]]; then
    type=${BASH_REMATCH[1],,}
    scope=${BASH_REMATCH[3]}
    bang=${BASH_REMATCH[4]}
    text=${BASH_REMATCH[5]}
  fi
  entry="- ${scope:+**$scope:** }$text ([\`${sha:0:7}\`]($repo_url/commit/$sha))"
  if [[ -n $bang || $body == *"BREAKING CHANGE"* ]]; then
    breaking+="$entry"$'\n'
    continue
  fi
  case $type in
    feat) key="feat" ;;
    fix) key="fix" ;;
    perf) key="perf" ;;
    refactor) key="refactor" ;;
    docs) key="docs" ;;
    test) key="test" ;;
    build | ci | chore | style | revert) key="chore" ;;
    *) key="other" ;;
  esac
  sections[$key]+="$entry"$'\n'
done < <(git log --no-merges --format='%H%x1f%s%x1f%b%x1e' "$range")

print_section() {
  local title=$1 content=$2
  [[ -n $content ]] || return 0
  printf '### %s\n\n%s\n' "$title" "$content"
}

printf '## Changes\n\n'
if [[ -z $breaking && ${#sections[@]} -eq 0 ]]; then
  printf 'No changes since %s.\n\n' "$previous"
fi
print_section "⚠️ Breaking changes" "$breaking"
print_section "✨ Features" "${sections[feat]:-}"
print_section "🐛 Bug fixes" "${sections[fix]:-}"
print_section "⚡ Performance" "${sections[perf]:-}"
print_section "♻️ Refactoring" "${sections[refactor]:-}"
print_section "📚 Documentation" "${sections[docs]:-}"
print_section "🧪 Tests" "${sections[test]:-}"
print_section "🔧 Maintenance" "${sections[chore]:-}"
print_section "📝 Other changes" "${sections[other]:-}"

if [[ -n $previous ]]; then
  printf '**Full changelog:** %s/compare/%s...%s\n\n' "$repo_url" "$previous" "$version"
else
  printf '**Full changelog:** %s/commits/%s\n\n' "$repo_url" "$version"
fi

printf '## Downloads\n\n'
printf 'Static binaries, built with `CGO_ENABLED=0` from the tagged commit:\n\n'
printf '| Platform | Archive |\n|---|---|\n'
for archive in "$dist"/*.tar.gz; do
  name=$(basename "$archive")
  platform=${name%.tar.gz}
  platform=${platform#zfsbackup_"${version#v}"_}
  os=${platform%_*}
  arch=${platform#*_}
  case $os in
    linux) os=Linux ;;
    freebsd) os=FreeBSD ;;
  esac
  case $arch in
    amd64) arch="x86-64" ;;
    arm64) arch="ARM64" ;;
  esac
  printf '| %s %s | [`%s`](%s/releases/download/%s/%s) |\n' "$os" "$arch" "$name" "$repo_url" "$version" "$name"
done

cat <<EOF

Every host needs OpenZFS 2.3 or newer. To install on Linux x86-64:

\`\`\`sh
curl -fLO $repo_url/releases/download/$version/zfsbackup_${version#v}_linux_amd64.tar.gz
curl -fLO $repo_url/releases/download/$version/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf zfsbackup_${version#v}_linux_amd64.tar.gz
sudo install -m 0755 zfsbackup_${version#v}_linux_amd64/zfsbackup /usr/local/bin/zfsbackup
\`\`\`

Each archive carries a [build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations). Verify it with \`gh attestation verify <archive> --repo $repo\`.
EOF
