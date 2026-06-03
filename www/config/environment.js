/* jshint node: true */

module.exports = function(environment) {
  var ENV = {
    modulePrefix: 'open-ethereum-pool',
    environment: environment,
    rootURL: '/',
    locationType: 'hash',
    EmberENV: {
      FEATURES: {},
      EXTEND_PROTOTYPES: false
    },

    APP: {
      // Leave ApiUrl empty to avoid double /api
      ApiUrl: '',
      HttpHost: 'http://pool.tkmchain.site',
      HttpPort: 8888,
      StratumHost: 'stratum.tkmchain.site',
      StratumPort: 8008,
      PoolFee: '1%',
      PayoutThreshold: '0.5 TKM',
      BlockTime: 120
    }
  };

  if (environment === 'development') {
    ENV.APP.ApiUrl = 'http://localhost:8080/';
  }

  if (environment === 'test') {
    ENV.locationType = 'none';
    ENV.APP.LOG_ACTIVE_GENERATION = false;
    ENV.APP.LOG_VIEW_LOOKUPS = false;
    ENV.APP.rootElement = '#ember-testing';
  }

  return ENV;
};
