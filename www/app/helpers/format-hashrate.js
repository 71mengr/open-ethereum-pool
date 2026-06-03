import Ember from 'ember';

export function formatHashrate(params, hash) {
  hash = hash || {};

  var fallback = hash.fallback === undefined ? '' : hash.fallback;
  var hashrate = Number(params[0]);

  if (!isFinite(hashrate)) {
    return fallback;
  }
  var i = 0;
  var units = ['H', 'KH', 'MH', 'GH', 'TH', 'PH'];
  while (hashrate > 1000) {
    hashrate = hashrate / 1000;
    i++;
  }
  return hashrate.toFixed(2) + ' ' + units[i];
}

export default Ember.Helper.helper(formatHashrate);
