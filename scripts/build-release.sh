#!/bin/sh

set -eu

version="${1:-}"
case "$version" in
	v[0-9]*)
		;;
	*)
		echo "usage: $0 vMAJOR.MINOR.PATCH" >&2
		exit 2
		;;
esac

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root"

commit="${GIT_COMMIT:-$(git rev-parse --short=12 HEAD)}"
build_date="${BUILD_DATE:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}"
dist_root="${DIST_DIR:-$repo_root/dist}"
release_dir="$dist_root/$version"

if [ -e "$release_dir" ]; then
	echo "release directory already exists: $release_dir" >&2
	echo "remove or move it before rebuilding the same version" >&2
	exit 1
fi

mkdir -p "$release_dir"

for target in \
	"darwin amd64" \
	"darwin arm64" \
	"linux amd64" \
	"linux arm64"
do
	set -- $target
	goos="$1"
	goarch="$2"
	package_name="feiq-cli_${version#v}_${goos}_${goarch}"
	package_dir="$release_dir/$package_name"

	mkdir -p "$package_dir"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
		-trimpath \
		-ldflags "-s -w -X main.appVersion=$version -X main.appCommit=$commit -X main.appDate=$build_date" \
		-o "$package_dir/feiq-cli" \
		./cmd/feiq-cli
	cp LICENSE README.md config.example.json "$package_dir/"
	tar -C "$release_dir" -czf "$release_dir/$package_name.tar.gz" "$package_name"
done

cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum ./*.tar.gz > checksums.txt
else
	shasum -a 256 ./*.tar.gz > checksums.txt
fi

echo "release artifacts created in $release_dir"
