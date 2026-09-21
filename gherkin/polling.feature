Feature: Poll cadence and overlapping polls
  As a consumer of the Dynatrace OpenFeature provider
  I want the provider to keep polling on a fixed cadence even when a fetch runs long
  So that a slow or unresponsive CDN is detected rather than hidden.

  Background:
    Given a mock server is running

  # ---------------------------------------------------------------------------
  # Cadence
  # ---------------------------------------------------------------------------
  @polling
  Scenario: The provider re-fetches on the poll interval
    Given an initialized, READY provider serving the "flags-v1" flag configuration
    When 3 poll intervals elapse
    Then the CDN has received 3 further requests
    And consecutive CDN requests are one poll interval apart

  # The interval is measured from when each request is initiated, not from when the previous one
  # completed (providers.md §2.1). A provider that waits for a slow fetch to finish before starting
  # the next one silently stretches its cadence during exactly the outage it is meant to detect.
  @polling
  @overlap
  Scenario: The poll cadence is anchored to request initiation, not completion
    Given an initialized, READY provider serving the "flags-v1" flag configuration
    And the CDN is programmed to respond slowly, taking longer than the poll interval
    When 3 poll intervals elapse
    Then the CDN has received 3 further requests
    And consecutive CDN requests are one poll interval apart

  # ---------------------------------------------------------------------------
  # Overlapping polls and the staleness grace period
  # ---------------------------------------------------------------------------
  # A tick that overlaps an in-flight fetch learns nothing, so it must emit neither success nor
  # failure (providers.md §2.4). Reporting it as a success resets the grace timer, which leaves the
  # provider stuck in READY for the whole outage.
  @polling
  @overlap
  @grace
  Scenario: An overlapped poll does not count as a successful poll
    Given an initialized, READY provider serving the "flags-v1" flag configuration
    And the CDN is programmed to respond slowly, taking longer than the grace period
    When the grace period elapses
    Then the provider state is "STALE"
    And flag "flagA" continues to evaluate to true
