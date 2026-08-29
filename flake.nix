{
  description = "bosun: the toolchain this repository builds, tests and proves itself with";

  inputs = {
    nixpkgs.url = "nixpkgs/nixos-unstable";
    # No `inputs.nixpkgs.follows` here: flake-utils dropped its nixpkgs input,
    # and overriding one that does not exist is a warning on every evaluation.
    utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, utils }:
    utils.lib.eachDefaultSystem (
      system:
        let
          pkgs = nixpkgs.legacyPackages.${system};

          # ------------------------------------------------------------------
          # helm and kubeconform are PINNED to the versions the images carry,
          # and taken from the same upstream releases rather than from nixpkgs.
          #
          # This is not fussiness. The gate shells out to helm to render a
          # chart, and both Dockerfiles say why they pin it: "two components
          # rendering the same chart with different Helms is a difference
          # nobody would think to look for". nixpkgs currently ships Helm 4,
          # so a dev shell that took `kubernetes-helm` would hand you a
          # different renderer than production for the one operation the whole
          # gate is built on, and the verdict you got locally would not be
          # the verdict CI or the cluster got.
          #
          # hack/portability-test.sh asserts these two strings equal the ARGs
          # in Dockerfile, so the pin cannot drift in silence. Bump them
          # together or the check fails.
          # ------------------------------------------------------------------
          helmVersion = "3.19.0";
          kubeconformVersion = "0.7.0";

          # Upstream's platform spelling, which both projects happen to share.
          plat = {
            aarch64-darwin = "darwin-arm64";
            x86_64-darwin = "darwin-amd64";
            x86_64-linux = "linux-amd64";
            aarch64-linux = "linux-arm64";
          }.${system};

          hashes = {
            helm = {
              aarch64-darwin = "sha256-MVE+EZPaTrSuBC61+Y75rKeJDPoTb0cHyNT3DiEVvvY=";
              x86_64-darwin = "sha256-CaEIwKvaQuRa8XK+ZcSRJTVL980Xjb4QQ16UVA5Jx7k=";
              x86_64-linux = "sha256-p/gc4IAHCRuG2L1pbrTYa40PLhufbHFL5i+C+WpZRJY=";
              aarch64-linux = "sha256-RAz3rdCu4n68k/rallUjwdwuCrNA1DSNoiFXN/wNdq0=";
            };
            kubeconform = {
              aarch64-darwin = "sha256-tdMrLLd/nHgcl2sgqF4tC8j5GE1dHP5mWi8xoZ+Z7rk=";
              x86_64-darwin = "sha256-xnccyJTYLhsS817nl9zaH32mo3h6owkCoVwmQFbdQNQ=";
              x86_64-linux = "sha256-wxUY3dEiZjs/Oqh0z+gXjLCYjelE8px0oLkmCSDRFdM=";
              aarch64-linux = "sha256-zJB8z548NFI/DzK2l0UmXgppCMqFuS9Bkx1FN4YOuDw=";
            };
          };

          # Both are static Go binaries, so there is nothing to build and
          # nothing to patch, so unpack and install.
          prebuilt = { pname, version, url, hash, path }:
            pkgs.stdenvNoCC.mkDerivation {
              inherit pname version;
              src = pkgs.fetchurl { inherit url hash; };
              sourceRoot = ".";
              # Never strip. These are signed upstream on darwin, and a
              # stripped signature does not fail honestly: the binary is
              # killed on launch with no explanation worth reading.
              dontStrip = true;
              installPhase = ''
                runHook preInstall
                install -Dm755 ${path} $out/bin/${pname}
                runHook postInstall
              '';
              meta.mainProgram = pname;
            };

          helm = prebuilt {
            pname = "helm";
            version = helmVersion;
            url = "https://get.helm.sh/helm-v${helmVersion}-${plat}.tar.gz";
            hash = hashes.helm.${system};
            path = "${plat}/helm";
          };

          kubeconform = prebuilt {
            pname = "kubeconform";
            version = kubeconformVersion;
            url = "https://github.com/yannh/kubeconform/releases/download/v${kubeconformVersion}/kubeconform-${plat}.tar.gz";
            hash = hashes.kubeconform.${system};
            path = "kubeconform";
          };
        in {
          packages = { inherit helm kubeconform; };

          devShells.default = pkgs.mkShell {
            packages = with pkgs; [
              # The module. go.mod requires 1.26.6, which is where the standard
              # library's currently-reachable advisories are fixed and therefore
              # what CI's govulncheck step needs to pass. nixpkgs is at 1.26.x
              # and may be behind that; GOTOOLCHAIN is `auto`, so the go here
              # fetches the newer toolchain rather than refusing to build. If
              # that ever has to become `local`, this pin moves to nixpkgs.
              go
              gopls

              # Rendering and validation, pinned above to image parity.
              helm
              kubeconform

              # The proving ground: local/scripts drive a kind cluster through
              # kubectl, and every script's assertions are python3 one-liners.
              kubectl
              kind
              python3
              jq
              yq-go
              git
              curl

              # The documentation site. Pinned to the major CI installs, because
              # `npm run build` is a required check and a site that only builds
              # on the runner is one nobody builds before pushing. It also owns
              # `npm run og`, whose output is committed, so a card regenerated
              # under a different Node is a diff nobody asked for.
              nodejs_22
            ];

            shellHook = ''
              # Speak only when something is missing. Two tools stay a host
              # concern rather than living in here:
              #
              #   colima      a macOS VM, with state in ~/.colima that a
              #                  second copy at a different version would be
              #                  managing behind the first one's back.
              #   idpbuilder  not in nixpkgs.
              #
              # local/scripts/00-runtime.sh installs both with brew, which is
              # what a macOS host is going to do anyway.
              missing=""
              command -v colima     >/dev/null 2>&1 || missing="$missing colima"
              command -v idpbuilder >/dev/null 2>&1 || missing="$missing idpbuilder"
              command -v docker     >/dev/null 2>&1 || missing="$missing docker"
              if [ -n "$missing" ]; then
                echo "note: the local proving ground also needs:$missing" >&2
                echo "      host tools, installed by local/scripts/00-runtime.sh; everything else is in this shell." >&2
              fi
            '';
          };
        }
    );
}
