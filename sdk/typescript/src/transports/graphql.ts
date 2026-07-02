import { z } from "zod";
import { TridentError } from "../errors.js";
import { SorobanEventSchema } from "../index.js";

// GraphQL query for listing events with pagination
const EVENTS_QUERY = `
  query GetEvents($contractId: String, $topic0: String, $topic1: String, $fromLedger: Int, $toLedger: Int, $first: Int, $after: String) {
    events(contractId: $contractId, topic0: $topic0, topic1: $topic1, fromLedger: $fromLedger, toLedger: $toLedger, first: $first, after: $after) {
      edges {
        cursor
        node {
          id
          contractId
          ledgerSequence
          ledgerTimestamp
          topic0
          topic1
          topic2
          topic3
          data
          transactionHash
          eventType
        }
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
`;

// GraphQL query for fetching a single event by ID
const EVENT_BY_ID_QUERY = `
  query GetEventById($id: String!) {
    eventById(id: $id) {
      id
      contractId
      ledgerSequence
      ledgerTimestamp
      topic0
      topic1
      topic2
      topic3
      data
      transactionHash
      eventType
      createdAt
    }
  }
`;

// GraphQL subscription for real-time events
const SUBSCRIBE_TO_CONTRACT_SUBSCRIPTION = `
  subscription SubscribeToContract($contractId: String!, $topic0: String) {
    contractEvents(contractId: $contractId, topic0: $topic0) {
      id
      contractId
      ledgerSequence
      ledgerTimestamp
      topic0
      topic1
      topic2
      topic3
      data
      transactionHash
      eventType
      createdAt
    }
  }
`;

interface GraphQLResponse<T> {
  data?: T;
  errors?: Array<{ message: string }>;
}

interface EventNode {
  id: string;
  contractId: string;
  ledgerSequence: number;
  ledgerTimestamp: string;
  topic0?: string;
  topic1?: string;
  topic2?: string;
  topic3?: string;
  data: string;
  transactionHash: string;
  eventType: string;
  createdAt?: string;
}

interface EventsQueryResult {
  events: {
    edges: Array<{
      cursor: string;
      node: EventNode;
    }>;
    pageInfo: {
      hasNextPage: boolean;
      endCursor: string | null;
    };
  };
}

interface EventByIdQueryResult {
  eventById: EventNode;
}

export class GraphQLTransport {
  private apiUrl: string;
  private apiKey: string;

  constructor(apiUrl: string, apiKey: string) {
    this.apiUrl = apiUrl;
    this.apiKey = apiKey;
  }

  private get headers(): Record<string, string> {
    return {
      "X-API-Key": this.apiKey,
      "Content-Type": "application/json",
    };
  }

  private async request<T>(query: string, variables: Record<string, unknown>): Promise<T> {
    const graphqlUrl = `${this.apiUrl}/graphql`;

    let res: Response;
    try {
      res = await fetch(graphqlUrl, {
        method: "POST",
        headers: this.headers,
        body: JSON.stringify({ query, variables }),
      });
    } catch (cause) {
      throw new TridentError("INTERNAL", "Network request failed", cause);
    }

    if (!res.ok) {
      const body = await res.text().catch(() => "");
      throw new TridentError(
        "SERVICE_UNAVAILABLE",
        `HTTP ${res.status}: ${body}`,
        undefined,
      );
    }

    const json: unknown = await res.json().catch((cause: unknown) => {
      throw new TridentError("INTERNAL", "Failed to parse response JSON", cause);
    });

    const response = json as GraphQLResponse<T>;

    // Check for GraphQL errors even if HTTP status is 200
    if (response.errors && response.errors.length > 0) {
      throw new TridentError(
        "INTERNAL",
        `GraphQL error: ${response.errors[0].message}`,
        undefined,
      );
    }

    if (!response.data) {
      throw new TridentError(
        "INTERNAL",
        "No data in GraphQL response",
        undefined,
      );
    }

    return response.data;
  }

  async queryEvents(
    contractId?: string,
    topic0?: string,
    topic1?: string,
    fromLedger?: number,
    toLedger?: number,
    limit?: number,
    after?: string,
  ) {
    const variables: Record<string, unknown> = {};
    if (contractId) variables.contractId = contractId;
    if (topic0) variables.topic0 = topic0;
    if (topic1) variables.topic1 = topic1;
    if (fromLedger !== undefined) variables.fromLedger = fromLedger;
    if (toLedger !== undefined) variables.toLedger = toLedger;
    if (limit !== undefined) variables.first = limit;
    if (after) variables.after = after;

    const result = await this.request<EventsQueryResult>(EVENTS_QUERY, variables);

    return {
      events: result.events.edges.map((edge) => this.nodeToSorobanEvent(edge.node)),
      cursor: result.events.pageInfo.endCursor,
      hasMore: result.events.pageInfo.hasNextPage,
    };
  }

  async getEventById(id: string) {
    const result = await this.request<EventByIdQueryResult>(EVENT_BY_ID_QUERY, { id });
    return this.nodeToSorobanEvent(result.eventById);
  }

  getSubscriptionQuery(contractId: string, topic0?: string): string {
    return SUBSCRIBE_TO_CONTRACT_SUBSCRIPTION;
  }

  getSubscriptionVariables(contractId: string, topic0?: string): Record<string, unknown> {
    const variables: Record<string, unknown> = { contractId };
    if (topic0) variables.topic0 = topic0;
    return variables;
  }

  private nodeToSorobanEvent(node: EventNode) {
    return SorobanEventSchema.parse({
      id: node.id,
      contractId: node.contractId,
      ledgerSequence: node.ledgerSequence,
      ledgerTimestamp: node.ledgerTimestamp,
      transactionHash: node.transactionHash,
      eventIndex: 0, // GraphQL response doesn't include eventIndex
      eventType: node.eventType,
      topics: [node.topic0, node.topic1, node.topic2, node.topic3].filter(Boolean),
      data: (() => {
        try {
          return JSON.parse(node.data);
        } catch {
          return node.data;
        }
      })(),
      createdAt: node.createdAt || new Date().toISOString(),
    });
  }
}
