"""The score of one payment."""

LIMIT_CENTS = 500_00


def score(payment):
    """Returns a risk score from 0 to 1."""
    risk = 0.0
    if payment["cents"] > LIMIT_CENTS:
        risk += 0.5
    if payment["country"] != payment["card_country"]:
        risk += 0.4
    return min(risk, 1.0)
