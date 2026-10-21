use std::net::SocketAddr;
use std::time::Duration;
use tonic_health::pb::health_check_response::ServingStatus;
use tonic_health::pb::health_client::HealthClient;
use tonic_health::pb::HealthCheckRequest;

#[tokio::test]
async fn test_grpc_health_service_check_and_status_transitions() {
    // 1. Pick an available local port
    let local_addr: SocketAddr = {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        listener.local_addr().unwrap()
    };

    let (mut reporter, health_service) = tonic_health::server::health_reporter();

    // Set initial serving status for overall service and trident.Events
    reporter
        .set_service_status("", tonic_health::ServingStatus::Serving)
        .await;
    reporter
        .set_service_status("trident.Events", tonic_health::ServingStatus::Serving)
        .await;

    // Spawn server in background
    let server_handle = tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(health_service)
            .serve(local_addr)
            .await
            .unwrap();
    });

    // Wait until server is accepting connections
    let mut channel = None;
    for _ in 0..50 {
        tokio::time::sleep(Duration::from_millis(20)).await;
        if let Ok(c) = tonic::transport::Channel::from_shared(format!("http://{}", local_addr))
            .unwrap()
            .connect()
            .await
        {
            channel = Some(c);
            break;
        }
    }
    let channel = channel.expect("failed to connect to local gRPC health service");
    let mut client = HealthClient::new(channel);

    // 2. Test Check on overall system ("")
    let resp = client
        .check(HealthCheckRequest {
            service: "".to_string(),
        })
        .await
        .expect("check root service failed")
        .into_inner();
    assert_eq!(resp.status, ServingStatus::Serving as i32);

    // 3. Test Check on trident.Events
    let resp = client
        .check(HealthCheckRequest {
            service: "trident.Events".to_string(),
        })
        .await
        .expect("check trident.Events failed")
        .into_inner();
    assert_eq!(resp.status, ServingStatus::Serving as i32);

    // 4. Test Check on unknown service returns NotFound status error per gRPC spec
    let err = client
        .check(HealthCheckRequest {
            service: "unknown.Service".to_string(),
        })
        .await
        .unwrap_err();
    assert_eq!(err.code(), tonic::Code::NotFound);

    // 5. Test state transition to NotServing
    reporter
        .set_service_status("", tonic_health::ServingStatus::NotServing)
        .await;
    reporter
        .set_service_status("trident.Events", tonic_health::ServingStatus::NotServing)
        .await;

    let resp = client
        .check(HealthCheckRequest {
            service: "".to_string(),
        })
        .await
        .expect("check root after downgrade failed")
        .into_inner();
    assert_eq!(resp.status, ServingStatus::NotServing as i32);

    let resp = client
        .check(HealthCheckRequest {
            service: "trident.Events".to_string(),
        })
        .await
        .expect("check trident.Events after downgrade failed")
        .into_inner();
    assert_eq!(resp.status, ServingStatus::NotServing as i32);

    server_handle.abort();
}
