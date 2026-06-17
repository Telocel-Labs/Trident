use chrono::{DateTime, Utc};
use sqlx::PgPool;
use tonic::{Request, Response, Status};
use uuid::Uuid;

use crate::trident::{
    events_server::Events, Event, GetEventRequest, ListEventsRequest, ListEventsResponse,
    StreamEventsRequest,
};

pub struct EventsServiceImpl {
    pub db: PgPool,
}

impl EventsServiceImpl {
    pub fn new(db: PgPool) -> Self {
        Self { db }
    }
}

// ---------------------------------------------------------------------------
// DB row → proto conversion
// ---------------------------------------------------------------------------

#[derive(sqlx::FromRow)]
struct EventRow {
    id: Uuid,
    contract_id: String,
    ledger_sequence: i64,
    ledger_timestamp: DateTime<Utc>,
    transaction_hash: String,
    event_index: i32,
    event_type: String,
    topics: serde_json::Value,
    data: serde_json::Value,
    created_at: DateTime<Utc>,
}

fn row_to_event(row: EventRow) -> Event {
    let topics: Vec<String> = serde_json::from_value(row.topics).unwrap_or_default();
    Event {
        id: row.id.to_string(),
        contract_id: row.contract_id,
        ledger_sequence: row.ledger_sequence as u64,
        ledger_timestamp: row.ledger_timestamp.to_rfc3339(),
        transaction_hash: row.transaction_hash,
        event_index: row.event_index as u32,
        event_type: row.event_type,
        topics,
        data: row.data.to_string(),
        created_at: row.created_at.to_rfc3339(),
    }
}

fn db_err(e: sqlx::Error) -> Status {
    tracing::error!(error = %e, "database error");
    Status::internal("internal error")
}

// ---------------------------------------------------------------------------
// gRPC service implementation
// ---------------------------------------------------------------------------

#[tonic::async_trait]
impl Events for EventsServiceImpl {
    async fn list_events(
        &self,
        request: Request<ListEventsRequest>,
    ) -> Result<Response<ListEventsResponse>, Status> {
        let req = request.into_inner();

        let limit = req.limit.clamp(1, 200) as i64;

        let (cursor_seq, cursor_idx): (Option<i64>, Option<i32>) = if req.cursor.is_empty() {
            (None, None)
        } else {
            let cursor_id = Uuid::parse_str(&req.cursor)
                .map_err(|_| Status::invalid_argument("cursor must be a valid UUID"))?;

            let row: Option<(i64, i32)> = sqlx::query_as(
                "SELECT ledger_sequence, event_index FROM soroban_events WHERE id = $1",
            )
            .bind(cursor_id)
            .fetch_optional(&self.db)
            .await
            .map_err(db_err)?;

            match row {
                Some((seq, idx)) => (Some(seq), Some(idx)),
                None => return Err(Status::invalid_argument("cursor references unknown event")),
            }
        };

        let rows: Vec<EventRow> = sqlx::query_as(
            r#"
            SELECT id, contract_id, ledger_sequence, ledger_timestamp,
                   transaction_hash, event_index, event_type, topics, data, created_at
            FROM soroban_events
            WHERE
                ($1::text = '' OR contract_id = $1)
                AND ($2::text = '' OR topic_0 = $2)
                AND ($3::text = '' OR topic_1 = $3)
                AND ($4::bigint = 0 OR ledger_sequence >= $4)
                AND ($5::bigint = 0 OR ledger_sequence <= $5)
                AND (
                    $6::bigint IS NULL
                    OR (ledger_sequence, event_index) > ($6, $7)
                )
            ORDER BY ledger_sequence ASC, event_index ASC
            LIMIT $8
            "#,
        )
        .bind(&req.contract_id)
        .bind(&req.topic_0)
        .bind(&req.topic_1)
        .bind(req.ledger_from as i64)
        .bind(req.ledger_to as i64)
        .bind(cursor_seq)
        .bind(cursor_idx)
        .bind(limit)
        .fetch_all(&self.db)
        .await
        .map_err(db_err)?;

        let has_more = rows.len() as i64 == limit;
        let next_cursor = if has_more {
            rows.last().map(|r| r.id.to_string()).unwrap_or_default()
        } else {
            String::new()
        };

        let events: Vec<Event> = rows.into_iter().map(row_to_event).collect();

        Ok(Response::new(ListEventsResponse {
            events,
            next_cursor,
            has_more,
        }))
    }

    async fn get_event(
        &self,
        request: Request<GetEventRequest>,
    ) -> Result<Response<Event>, Status> {
        let req = request.into_inner();

        let id = Uuid::parse_str(&req.id)
            .map_err(|_| Status::invalid_argument("id must be a valid UUID"))?;

        let row: Option<EventRow> = sqlx::query_as(
            r#"
            SELECT id, contract_id, ledger_sequence, ledger_timestamp,
                   transaction_hash, event_index, event_type, topics, data, created_at
            FROM soroban_events
            WHERE id = $1
            "#,
        )
        .bind(id)
        .fetch_optional(&self.db)
        .await
        .map_err(db_err)?;

        match row {
            Some(r) => Ok(Response::new(row_to_event(r))),
            None => Err(Status::not_found(format!("event {id} not found"))),
        }
    }

    type StreamEventsStream = tokio_stream::wrappers::ReceiverStream<Result<Event, Status>>;

    async fn stream_events(
        &self,
        request: Request<StreamEventsRequest>,
    ) -> Result<Response<Self::StreamEventsStream>, Status> {
        let _req = request.into_inner();
        Err(Status::unimplemented("stream_events not yet implemented"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    macro_rules! require_db {
        () => {
            match std::env::var("TEST_DATABASE_URL") {
                Ok(url) => url,
                Err(_) => {
                    eprintln!("SKIP: TEST_DATABASE_URL not set");
                    return;
                }
            }
        };
    }

    async fn seed_events(pool: &PgPool, contract_id: &str, count: usize) {
        for i in 0..count {
            sqlx::query(
                r#"
                INSERT INTO soroban_events
                    (contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
                     event_index, event_type, topics, data)
                VALUES ($1, $2, NOW(), $3, $4, 'contract', '["transfer"]', '{}')
                ON CONFLICT DO NOTHING
                "#,
            )
            .bind(contract_id)
            .bind((100 + i) as i64)
            .bind(format!("txhash{i}"))
            .bind(i as i32)
            .execute(pool)
            .await
            .unwrap();
        }
    }

    async fn insert_one_event(pool: &PgPool) -> Uuid {
        let id = Uuid::new_v4();
        sqlx::query(
            r#"
            INSERT INTO soroban_events
                (id, contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
                 event_index, event_type, topics, data)
            VALUES ($1, 'CTEST', 999, NOW(), 'txhashtest', 0, 'contract', '["transfer"]', '{}')
            ON CONFLICT DO NOTHING
            "#,
        )
        .bind(id)
        .execute(pool)
        .await
        .unwrap();
        id
    }

    #[tokio::test]
    async fn list_events_filters_by_contract_id() {
        let db_url = require_db!();
        let pool = PgPool::connect(&db_url).await.unwrap();

        let contract_a = format!("CONTRACT_A_{}", uuid::Uuid::new_v4());
        let contract_b = format!("CONTRACT_B_{}", uuid::Uuid::new_v4());

        seed_events(&pool, &contract_a, 3).await;
        seed_events(&pool, &contract_b, 2).await;

        let svc = EventsServiceImpl::new(pool);
        let req = Request::new(ListEventsRequest {
            contract_id: contract_a.clone(),
            limit: 200,
            ..Default::default()
        });
        let resp = svc.list_events(req).await.unwrap().into_inner();

        assert_eq!(resp.events.len(), 3);
        assert!(resp.events.iter().all(|e| e.contract_id == contract_a));
        assert!(!resp.has_more);
    }

    #[tokio::test]
    async fn list_events_cursor_pagination() {
        let db_url = require_db!();
        let pool = PgPool::connect(&db_url).await.unwrap();

        let contract_id = format!("CONTRACT_PAGE_{}", uuid::Uuid::new_v4());
        seed_events(&pool, &contract_id, 5).await;

        let svc = EventsServiceImpl::new(pool);

        let first_page = svc
            .list_events(Request::new(ListEventsRequest {
                contract_id: contract_id.clone(),
                limit: 2,
                ..Default::default()
            }))
            .await
            .unwrap()
            .into_inner();

        assert_eq!(first_page.events.len(), 2);
        assert!(first_page.has_more);
        assert!(!first_page.next_cursor.is_empty());

        let second_page = svc
            .list_events(Request::new(ListEventsRequest {
                contract_id: contract_id.clone(),
                limit: 200,
                cursor: first_page.next_cursor,
                ..Default::default()
            }))
            .await
            .unwrap()
            .into_inner();

        assert_eq!(second_page.events.len(), 3);
        assert!(!second_page.has_more);
    }

    #[tokio::test]
    async fn get_existing_event_returns_correct_fields() {
        let db_url = require_db!();
        let pool = PgPool::connect(&db_url).await.unwrap();

        let event_id = insert_one_event(&pool).await;

        let svc = EventsServiceImpl::new(pool);
        let req = Request::new(GetEventRequest {
            id: event_id.to_string(),
        });
        let event = svc.get_event(req).await.unwrap().into_inner();

        assert_eq!(event.id, event_id.to_string());
        assert_eq!(event.contract_id, "CTEST");
        assert_eq!(event.event_type, "contract");
        assert_eq!(event.topics, vec!["transfer".to_string()]);
    }

    #[tokio::test]
    async fn get_unknown_uuid_returns_not_found() {
        let db_url = require_db!();
        let pool = PgPool::connect(&db_url).await.unwrap();
        let svc = EventsServiceImpl::new(pool);

        let req = Request::new(GetEventRequest {
            id: Uuid::new_v4().to_string(),
        });
        let err = svc.get_event(req).await.unwrap_err();

        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn get_malformed_uuid_returns_invalid_argument() {
        let db_url = require_db!();
        let pool = PgPool::connect(&db_url).await.unwrap();
        let svc = EventsServiceImpl::new(pool);

        let req = Request::new(GetEventRequest {
            id: "not-a-uuid".to_string(),
        });
        let err = svc.get_event(req).await.unwrap_err();

        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }
}
