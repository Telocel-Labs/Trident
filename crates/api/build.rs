fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Use the vendored protoc binary so neither CI nor local dev need a system install.
    let protoc_path = protoc_bin_vendored::protoc_bin_path()?;
    std::env::set_var("PROTOC", protoc_path);

    tonic_build::configure()
        .build_server(true)
        .build_client(false)
        .compile_protos(&["../../proto/trident.proto"], &["../../proto"])?;

    Ok(())
}
