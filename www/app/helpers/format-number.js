import Ember from 'ember';

function optionNumber(value) {
  if (value === undefined || value === null || value === '') {
    return undefined;
  }

  return parseInt(value, 10);
}

export function formatNumber(params, hash) {
  hash = hash || {};

  var value = params[0];
  var fallback = hash.fallback;

  if (value === undefined || value === null || value === '') {
    return fallback === undefined ? '' : fallback;
  }

  var number = Number(value);

  if (isNaN(number)) {
    return fallback === undefined ? value : fallback;
  }

  var options = {};
  var minimumFractionDigits = optionNumber(hash.minimumFractionDigits);
  var maximumFractionDigits = optionNumber(hash.maximumFractionDigits);

  if (hash.style) {
    options.style = hash.style;
  }
  if (minimumFractionDigits !== undefined) {
    options.minimumFractionDigits = minimumFractionDigits;
  }
  if (maximumFractionDigits !== undefined) {
    options.maximumFractionDigits = maximumFractionDigits;
  }

  return number.toLocaleString(undefined, options);
}

export default Ember.Helper.helper(formatNumber);
