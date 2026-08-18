@sse
Feature: SSE change notifications
  As a consumer of the Dynatrace OpenFeature provider (Java / Go / Python)
  I want the provider to open an SSE stream advertised by the CDN config and
  react to change notifications by re-fetching, while degrading gracefully when
  the stream drops
  So that flag changes are picked up promptly without relying on aggressive polling.

  Background:
    Given a mock server with SSE enabled is running

  # ---------------------------------------------------------------------------
  # Stream discovery from CDN config
  # ---------------------------------------------------------------------------
  @discovery
  Scenario: A direct SSE url in the config opens a stream
    Given a well-formed server SDK key
    And the CDN serves the "flags-v1" flag configuration advertising an SSE stream at url "http://<mockserver>/sse"
    When the provider is initialized
    Then the provider state is "READY"
    And the provider opens an SSE connection to "/sse"

  @discovery
  Scenario: A structured SSE endpoint resolves its origin from the CDN origin
    Given a well-formed server SDK key
    And the CDN serves the "flags-v1-sse" flag configuration
    When the provider is initialized
    Then the provider state is "READY"
    And the provider opens an SSE connection to "/sse"

  @discovery
  Scenario: No SSE stream in the config runs in polling-only mode
    Given a well-formed server SDK key
    And the CDN serves the "flags-v1" flag configuration
    When the provider is initialized
    Then the provider state is "READY"
    And the provider opens no SSE connection

  @discovery
  @config
  Scenario: SSE disabled by configuration ignores an advertised stream
    Given a well-formed server SDK key
    And SSE is disabled by configuration
    And the CDN serves the "flags-v1-sse" flag configuration
    When the provider is initialized
    Then the provider state is "READY"
    And the provider opens no SSE connection

  @discovery
  @resilience
  Scenario: A stream that fails to open falls back to polling and still reaches READY
    Given a well-formed server SDK key
    And the CDN serves the "flags-v1" flag configuration advertising an SSE stream at url "http://127.0.0.1:1/sse"
    When the provider is initialized
    Then initialization succeeds
    And the provider state is "READY"
    And the provider opens no SSE connection

  # ---------------------------------------------------------------------------
  # Connection lifecycle: connect / reconnect
  # ---------------------------------------------------------------------------
  @lifecycle
  @connect
  Scenario: Connecting the SSE stream triggers an immediate catch-up fetch
    Given a well-formed server SDK key
    And the CDN serves the "flags-v1-sse" flag configuration
    When the provider is initialized
    # 1st fetch in initialize, 2nd fetch on successful sse connection, both unconditional
    Then the CDN received 2 requests

  @lifecycle
  @connect
  @polling
  Scenario: Poll cadence relaxes to the safety-net interval while SSE is connected
    Given a well-formed server SDK key
    And the CDN serves the "flags-v1-sse" flag configuration
    When the provider is initialized
    Then the active poll interval is the SSE-connected interval

  @lifecycle
  @reconnect
  Scenario: A brief reconnect blip does not leave READY and pulls a catch-up fetch
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the SSE stream drops and reconnects within the disconnect debounce window
    Then the provider state is "READY"
    # 1st fetch in initialize, 2nd fetch on successful sse connection, 3rd on reconnection
    And the CDN recieved 3 requests

  # ---------------------------------------------------------------------------
  # Message handling: refetchConfig triggers an immediate conditional GET
  # ---------------------------------------------------------------------------
  @message
  @refetch
  Scenario: A refetchConfig message triggers an immediate conditional re-fetch
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    And flag "flagA" evaluates to true
    And the CDN responds with status 200 and the "flags-v2-sse" flag configuration with "Last-Modified" header "Tue, 02 Jan 2024 00:00:00 GMT"
    When the server emits an SSE message "{ "type": "refetchConfig", "lastModified": 1704153600 }"
    # 1704153600 = Tue, 02 Jan 2024 00:00:00 GMT, lastModified is ignored
    Then the CDN received 3 requests
    And a PROVIDER_CONFIGURATION_CHANGED event is emitted with changed flag "flagA"
    And flag "flagA" eventually evaluates to false

  @message
  @refetch
  @conditional-headers
  Scenario: The SSE-triggered re-fetch is conditional
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration with "ETag" header "v1"
    When the server emits an SSE message "{ "type": "refetchConfig", "lastModified": 1704153600 }"
    # 1st fetch in initialize, 2nd fetch on successful sse connection, both unconditional
    Then the CDN received 3 requests
    And the 3. CDN request has the "If-None-Match" header "v1"

  @message
  @refetch
  Scenario: A 304 to an SSE-triggered re-fetch leaves the config unchanged
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    And flag "flagA" evaluates to true
    And the CDN responds with status 304
    When the server emits an SSE message "{ "type": "refetchConfig", "lastModified": 1704153600 }"
    Then the provider state is "READY"
    And flag "flagA" continues to evaluate to true

  # ---------------------------------------------------------------------------
  # Message handling: non-actionable messages are ignored
  # ---------------------------------------------------------------------------
  @message
  @ignored
  Scenario Outline: Messages that are not actionable trigger no re-fetch
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the server emits an SSE message "<payload>"
    Then the provider state is "READY"
    # 1st fetch in initialize, 2nd fetch on successful sse connection, both unconditional - no 3rd request for invalid messages
    And the CDN received 2 requests

    Examples:
      | payload                        | note                        |
      | { "type": "unknownType" }      | unhandled type ignored      |
      | { "lastModified": 1704153600 } | missing type dropped        |
      | not-json                       | unparseable payload dropped |
      | {}                             | empty object dropped        |

  @message
  @envelope
  Scenario Outline: Both the Ably envelope and flat message shapes are accepted
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    And the CDN responds with status 200 and the "flags-v2-sse" flag configuration with "Last-Modified" header "Tue, 02 Jan 2024 00:00:00 GMT"
    When the server emits an SSE message "<payload>"
    Then the provider state is "READY"
    And the CDN received 3 requests
    And flag "flagA" eventually evaluates to false

    Examples:
      | payload                                                                   | shape         |
      | { "type": "refetchConfig", "etag": "\"v2\"", "lastModified": 1704153600 } | flat          |
      | { "data": "{\"type\":\"refetchConfig\",\"lastModified\":1704153600}" }    | Ably envelope |

  # ---------------------------------------------------------------------------
  # Connection lifecycle: sustained disconnect, staleness, grace, recovery
  # ---------------------------------------------------------------------------
  @lifecycle
  @disconnect
  @polling
  Scenario: A sustained disconnect switches to the aggressive poll cadence and goes STALE
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the SSE stream drops and stays down past the disconnect debounce window of 5 seconds
    Then the provider state is "STALE"
    And a PROVIDER_STALE event is emitted
    And the active poll interval is the SSE-disconnected interval of 10 seconds

  @lifecycle
  @disconnect
  @recovery
  Scenario: A successful fetch while STALE recovers to READY
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the SSE stream drops and stays down past the disconnect debounce window of 5 seconds
    Then the provider state is "STALE"
    And a PROVIDER_STALE event is emitted
    When polling triggers a configuration refetch
    Then the provider state is "READY"
    And a PROVIDER_READY event is emitted

  @lifecycle
  @disconnect
  @recovery
  Scenario: Reconnecting the SSE stream while STALE recovers to READY
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the SSE stream drops and stays down past the disconnect debounce window of 5 seconds
    Then the provider state is "STALE"
    And a PROVIDER_STALE event is emitted
    When the SSE stream reconnects
    Then the provider state is "READY"
    And a PROVIDER_READY event is emitted

  @lifecycle
  @disconnect
  @grace
  Scenario: A disconnect that never recovers transitions STALE to ERROR after the grace period
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the SSE stream drops and stays down past the disconnect debounce window of 5 seconds
    Then the provider state is "STALE"
    And a PROVIDER_STALE event is emitted
    And the CDN responds with status 500
    When the grace period expires with the CDN still unreachable
    Then the provider state is "ERROR"
    And a PROVIDER_ERROR event is emitted

  @lifecycle
  @disconnect
  Scenario: A poll failure while SSE is healthy stays READY
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    And the CDN responds with status 500
    When polling triggers a configuration refetch
    Then the provider state is "READY"

  # ---------------------------------------------------------------------------
  # Shutdown
  # ---------------------------------------------------------------------------
  @lifecycle
  @shutdown
  Scenario: Shutdown closes the SSE stream
    Given an initialized, READY provider serving the "flags-v1-sse" flag configuration
    When the provider is shut down
    Then the provider closes the SSE connection
    And messages emitted after shutdown trigger no CDN request
