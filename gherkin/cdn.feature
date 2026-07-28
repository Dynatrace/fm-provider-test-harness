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
  # Key validation (no network I/O)
  # ---------------------------------------------------------------------------

  @startup @key-validation
  Scenario: Valid SDK key initializes and reaches READY
    Given the SDK key "dt01.server_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5"
    And the CDN serves a valid flag configuration
    When the provider is constructed
    Then construction succeeds

  @startup @key-validation
  Scenario Outline: Invalid SDK key is rejected before any network call
    Given the SDK key "<key>"
    When the provider is constructed
    Then construction fails with a key-validation error
    And no request is made to the CDN

    Examples:
      | key                                                                       | note                          |
      |                                                                           | blank                         |
      | not-a-key                                                                 | no structure                  |
      | dt01.server_us_abcdef1234                                                 | missing secret/sha            |
      | dt01.server_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d                | missing sha suffix            |
      | dt01.web_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5        | web key rejected by server    |
      | dt01.mobile_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5     | mobile key rejected by server |

  # ---------------------------------------------------------------------------
  # CDN URL derivation
  # ---------------------------------------------------------------------------

  @startup @cdn-url
  Scenario Outline: CDN URL is derived from the geo encoded in the key
    Given the SDK key "<key>"
    And the CDN serves a valid flag configuration
    When the provider is initialized
    Then the derived CDN host is "cdn.<geo>.fm.dynatrace.com"
    And the fetch path is "/server/<key>.json"

    Examples:
      | key                                                                     | geo |
      | dt01.server_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5   | us  |
      | dt01.server_eu_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5   | eu  |

  @startup @cdn-url
  Scenario: Config origin override redirects the fetch away from the production CDN
    Given the SDK key "dt01.server_us_abcdef1234.de848e97a9cc4cc78aae568e65f49a9d_a1b2c3d4e5"
    And the config origin is overridden to "https://cdn.eu.fm.dynatracelabs.com"
    And the CDN serves a valid flag configuration
    When the provider is initialized
    Then the derived CDN host is "cdn.eu.fm.dynatracelabs.com"

  # ---------------------------------------------------------------------------
  # Initial fetch outcomes
  # ---------------------------------------------------------------------------

  @startup @initial-fetch
  Scenario: Successful initial fetch loads flags and reaches READY
    Given a well-formed server SDK key
    And the CDN serves a valid flag configuration
    When the provider is initialized
    Then initialization succeeds
    And the provider state is READY
    And flag "flagA" evaluates to true

  @startup @initial-fetch @conditional-headers
  Scenario: The initial fetch is unconditional
    Given a well-formed server SDK key
    And the CDN serves a valid flag configuration
    When the provider is initialized
    Then the initial CDN request has no "If-None-Match" header
    And the initial CDN request has no "If-Modified-Since" header
    And the initial CDN request returns 200

  @startup @initial-fetch @failure
  Scenario: A 5xx initial response fails initialization
    Given a well-formed server SDK key
    And the CDN responds with status 500
    When the provider is initialized
    Then initialization fails
    And the provider does not become READY

  @startup @initial-fetch @failure
  Scenario Outline: An invalid initial body fails initialization
    Given a well-formed server SDK key
    And the CDN responds with status 200 and body <body>
    When the provider is initialized
    Then initialization fails
    And the provider does not become READY

    Examples:
      | body                             |
      | ""                               |
      | "this-is-not-valid-flag-config"  |
      | null                             |

  # ---------------------------------------------------------------------------
  # Conditional revalidation on subsequent fetches
  # ---------------------------------------------------------------------------

  @polling @revalidation
  Scenario: A 304 response leaves the active config unchanged and keeps READY
    Given an initialized, READY provider serving a valid flag configuration
    And flag "flagA" evaluates to true
    And the CDN responds with status 304
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" continues to evaluate to true
    And the CDN received at least 2 requests

  @polling @revalidation
  Scenario: A 200 with a strictly newer Last-Modified applies the update
    Given an initialized, READY provider serving a valid flag configuration with Last-Modified "Mon, 01 Jan 2024 00:00:00 GMT"
    And the CDN responds with status 200 and a changed configuration with Last-Modified "Tue, 02 Jan 2024 00:00:00 GMT"
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" eventually evaluates to false

  @polling @revalidation @stale-node
  Scenario: A 200 whose Last-Modified is not newer is ignored (stale-node defense)
    Given an initialized, READY provider serving a valid flag configuration with Last-Modified "Tue, 02 Jan 2024 00:00:00 GMT"
    And the CDN responds with status 200 and a changed configuration with Last-Modified "Mon, 01 Jan 2024 00:00:00 GMT"
    When polling triggers a configuration refetch
    Then flag "flagA" continues to evaluate to true
    And the provider state is READY

  # ---------------------------------------------------------------------------
  # Rate limiting and retry
  # ---------------------------------------------------------------------------

  @polling @rate-limit
  Scenario: A 429 with Retry-After suspends fetching and serves cached config
    Given an initialized, READY provider serving a valid flag configuration
    And the CDN responds with status 429 and Retry-After 30 seconds
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" continues to evaluate to true
    And the CDN receives no further requests while rate-limited

  @polling @retry
  Scenario: A transient 5xx is retried with backoff and recovers on 2xx
    Given an initialized, READY provider serving a valid flag configuration
    And the CDN responds 500 then 200 with a changed configuration
    When polling triggers a configuration refetch
    Then the provider state is READY
    And flag "flagA" eventually evaluates to false