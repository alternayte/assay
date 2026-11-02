"""The scoring service."""

from fraud.rules import score


class Scorer:
    """Scores payments and flags the risky ones."""

    threshold = 0.8

    def flag(self, payment):
        """Reports whether a payment needs a manual check."""
        return score.score(payment) >= self.threshold
