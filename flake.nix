{
  description = "A monitor for etcd in cloud k8s deployments";

  inputs.nixpkgs.url = "nixpkgs";

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [ "x86_64-linux" "x86_64-darwin" "aarch64-linux" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
      nixpkgsFor = forAllSystems (system: import nixpkgs { inherit system; });
    in {
      packages = forAllSystems (system: let
        pkgs = nixpkgsFor.${system};
      in {
        etcdmon = pkgs.buildGo119Module {
          pname = "etcdmon";
          version = "0.0.1";
          src = ./.;
          vendorSha256 = "sha256-hk0wfcoPx2OGUCh6Fh55S2P3ZsAfCM1ihYyR7iSSV40";

          checkInputs = [ pkgs.kind ];
        };
      });

      # The default package for 'nix build'. This makes sense if the
      # flake provides only one package or there is a clear "main"
      # package.
      defaultPackage = forAllSystems (system: self.packages.${system}.etcdmon);
    };
}
