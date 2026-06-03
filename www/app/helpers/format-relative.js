import Ember from 'ember';

var UNITS_IN_SECONDS = {
  second: 1,
  minute: 60,
  hour: 60 * 60,
  day: 60 * 60 * 24,
  month: 60 * 60 * 24 * 30,
  year: 60 * 60 * 24 * 365
};

function normalizeUnit(unit) {
  if (unit && UNITS_IN_SECONDS[unit]) {
    return unit;
  }

  return null;
}

function pluralize(value, unit) {
  if (value === 1) {
    return unit;
  }

  return unit + 's';
}

function bestUnit(seconds) {
  if (seconds < UNITS_IN_SECONDS.minute) {
    return 'second';
  }
  if (seconds < UNITS_IN_SECONDS.hour) {
    return 'minute';
  }
  if (seconds < UNITS_IN_SECONDS.day) {
    return 'hour';
  }
  if (seconds < UNITS_IN_SECONDS.month) {
    return 'day';
  }
  if (seconds < UNITS_IN_SECONDS.year) {
    return 'month';
  }

  return 'year';
}

export function formatRelative(params, hash) {
  hash = hash || {};

  var timestamp = params[0];
  var date = new Date(timestamp);
  var time = date.getTime();

  if (!timestamp || isNaN(time)) {
    return '';
  }

  var diffInSeconds = Math.round((time - Date.now()) / 1000);
  var absSeconds = Math.abs(diffInSeconds);
  var unit = normalizeUnit(hash.units) || bestUnit(absSeconds);
  var value = Math.round(absSeconds / UNITS_IN_SECONDS[unit]);

  if (value === 0) {
    if (absSeconds === 0) {
      return 'just now';
    }

    value = 1;
  }

  if (diffInSeconds < 0) {
    return value + ' ' + pluralize(value, unit) + ' ago';
  }

  return 'in ' + value + ' ' + pluralize(value, unit);
}

export default Ember.Helper.helper(formatRelative);
