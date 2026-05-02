# Producer-shipped cmake helper. install(FILES) below copies
# this into lib/cmake/prod/ alongside the synthesized
# prodConfig.cmake. Downstream consumers find_package(prod
# CONFIG) gets prod_DIR pointed at the staged dir;
# include(${prod_DIR}/Helpers.cmake) loads the function below.

function(prod_helper text)
    message(STATUS "prod_helper: ${text}")
endfunction()
