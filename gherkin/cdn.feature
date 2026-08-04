Feature: Provider startup and configuration fetching
  As a consumer of the Dynatrace OpenFeature provider (Java / Go / Python)
  I want the provider to validate its key, fetch flag config from the CDN, and
  keep it fresh via conditional requests
  So that evaluations are served from a correct, up-to-date configuration.

  # SDK key format: dt{NN}.server_{geo}_{rand}.{secret}_{sha}
  #   e.g. dt01.server_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5
  # The geo segment ("us", "eu", ...) drives the production CDN host:
  #   https://cdn.{geo}.fm.dynatrace.com/server/{key}.json
  Background:
    Given a mock server is running

  # ---------------------------------------------------------------------------
  # Initial fetch outcomes
  # ---------------------------------------------------------------------------
  @startup
  @initial-fetch
  Scenario: Successful initial fetch loads flags and reaches READY
    Given a well-formed server SDK key
    And the CDN serves a valid flag configuration
    When the provider is initialized
    Then initialization succeeds
    And the provider state is READY
    And flag "flagA" evaluates to true

  @startup
  @initial-fetch
  @conditional-headers
  Scenario: The initial fetch is unconditional
    Given a well-formed server SDK key
    And the CDN serves a valid flag configuration
    When the provider is initialized
    Then the initial CDN request has no "If-None-Match" header
    And the initial CDN request has no "If-Modified-Since" header
    And the initial CDN request returns 200

  @startup
  @initial-fetch
  @failure
  Scenario: A 5xx initial response fails initialization
    Given a well-formed server SDK key
    And the CDN responds with status 500
    When the provider is initialized
    Then initialization fails
    And the provider state is ERROR

  @startup
  @initial-fetch
  @failure
  Scenario Outline: An invalid initial body fails initialization
    Given a well-formed server SDK key
    And the CDN responds with status 200 and body <body>
    When the provider is initialized
    Then initialization fails
    And the provider state is ERROR

    Examples:
      | body                            |
      | ""                              |
      | "this-is-not-valid-flag-config" |
      | null                            |

  # ---------------------------------------------------------------------------
  # Conditional revalidation on subsequent fetches
  # ---------------------------------------------------------------------------
  @polling
  @revalidation
  Scenario: A 304 response leaves the active config unchanged and keeps READY
    Given an initialized, READY provider serving a valid flag configuration
    And flag "flagA" evaluates to true
    And the CDN responds with status 304
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" continues to evaluate to true
    And the CDN received at least 2 requests

  @polling
  @revalidation
  Scenario: A 200 with a strictly newer Last-Modified applies the update
    Given an initialized, READY provider serving a valid flag configuration with Last-Modified "Mon, 01 Jan 2024 00:00:00 GMT"
    And the CDN responds with status 200 and a changed configuration with Last-Modified "Tue, 02 Jan 2024 00:00:00 GMT"
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" eventually evaluates to false

  @polling
  @revalidation
  @stale-node
  Scenario: A 200 whose Last-Modified is not newer is ignored (stale-node defense)
    Given an initialized, READY provider serving a valid flag configuration with Last-Modified "Tue, 02 Jan 2024 00:00:00 GMT"
    And the CDN responds with status 200 and a changed configuration with Last-Modified "Mon, 01 Jan 2024 00:00:00 GMT"
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" continues to evaluate to true

  # ---------------------------------------------------------------------------
  # Rate limiting and retry
  # ---------------------------------------------------------------------------
  @polling
  @rate-limit
  Scenario: A 429 with Retry-After suspends fetching and serves cached config
    Given an initialized, READY provider serving a valid flag configuration
    And the CDN responds with status 429 and Retry-After 30 seconds
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" continues to evaluate to true
    And the CDN receives no further requests while rate-limited

  @polling
  @retry
  Scenario: A transient 5xx is retried with backoff and recovers on 2xx
    Given an initialized, READY provider serving a valid flag configuration
    And the CDN responds 500 then 200 with a changed configuration
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" eventually evaluates to false

  # ---------------------------------------------------------------------------
  # Flag evaluation
  # ---------------------------------------------------------------------------
  @evaluation
  Scenario: A known flag evaluates correctly
    Given an initialized, READY provider serving a valid flag configuration
    Then flag "flagA" evaluates to true

  @evaluation
  Scenario: An unknown flag returns a Flag-Not-Found error
    Given an initialized, READY provider serving a valid flag configuration
    Then evaluating flag "no-such-flag" returns a Flag-Not-Found error
